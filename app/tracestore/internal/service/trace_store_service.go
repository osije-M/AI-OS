package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	tracev1 "github.com/osije/ai-os/api/gen/go/trace/v1"
	"github.com/osije/ai-os/app/tracestore/internal/conf"
	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（无 cgo，兼容 CGO_ENABLED=0 的 docker 构建）
	"google.golang.org/protobuf/encoding/protojson"
)

// TraceStoreServiceImpl 以 SQLite 持久化执行链路 trace。
// gRPC 契约（Save/Get/List/Close）与旧 JSONL 版完全一致；换存储层是为了可查询/可统计。
type TraceStoreServiceImpl struct {
	tracev1.UnimplementedTraceStoreServer
	mu        sync.Mutex // 保护写（SQLite 单写者）
	db        *sql.DB
	storeFile string
}

const _schema = `
CREATE TABLE IF NOT EXISTS traces (
  trace_id   TEXT PRIMARY KEY,
  task       TEXT,
  status     TEXT,
  route      TEXT,
  elapsed_ms INTEGER,
  started_at INTEGER,
  ended_at   INTEGER,
  output     TEXT,
  steps_json TEXT
);
CREATE INDEX IF NOT EXISTS idx_traces_ended_at ON traces(ended_at);
CREATE INDEX IF NOT EXISTS idx_traces_status   ON traces(status);
CREATE INDEX IF NOT EXISTS idx_traces_route    ON traces(route);
`

func NewTraceStoreServiceImpl(cfg *conf.Config) (*TraceStoreServiceImpl, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.StoreFile), 0755); err != nil {
		return nil, err
	}
	// WAL + busy_timeout：并发读写更稳，写锁竞争时等待而非立即失败。
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", cfg.StoreFile)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(_schema); err != nil {
		db.Close()
		return nil, err
	}
	var n int
	_ = db.QueryRow("SELECT COUNT(*) FROM traces").Scan(&n)
	log.Printf("[tracestore] sqlite ready at %s (%d traces)", cfg.StoreFile, n)
	return &TraceStoreServiceImpl{db: db, storeFile: cfg.StoreFile}, nil
}

// marshalSteps 把 []*Step 序列化为 protojson 数组字符串。
func marshalSteps(steps []*tracev1.Step) (string, error) {
	if len(steps) == 0 {
		return "[]", nil
	}
	parts := make([]string, 0, len(steps))
	for _, st := range steps {
		b, err := protojson.Marshal(st)
		if err != nil {
			return "", err
		}
		parts = append(parts, string(b))
	}
	return "[" + strings.Join(parts, ",") + "]", nil
}

// unmarshalSteps 反序列化 protojson 数组字符串为 []*Step。
func unmarshalSteps(s string) []*tracev1.Step {
	if s == "" {
		return nil
	}
	var raws []json.RawMessage
	if err := json.Unmarshal([]byte(s), &raws); err != nil {
		log.Printf("[tracestore] steps unmarshal error: %v", err)
		return nil
	}
	out := make([]*tracev1.Step, 0, len(raws))
	for _, r := range raws {
		st := &tracev1.Step{}
		if err := protojson.Unmarshal(r, st); err != nil {
			log.Printf("[tracestore] step unmarshal error: %v", err)
			continue
		}
		out = append(out, st)
	}
	return out
}

func (s *TraceStoreServiceImpl) Save(ctx context.Context, req *tracev1.SaveRequest) (*tracev1.SaveReply, error) {
	t := req.Trace
	if t == nil {
		return &tracev1.SaveReply{Ok: false}, nil
	}
	stepsJSON, err := marshalSteps(t.Steps)
	if err != nil {
		log.Printf("[tracestore] marshal steps error: %v", err)
		return &tracev1.SaveReply{Ok: false}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx, `
INSERT INTO traces (trace_id, task, status, route, elapsed_ms, started_at, ended_at, output, steps_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(trace_id) DO UPDATE SET
  task=excluded.task, status=excluded.status, route=excluded.route,
  elapsed_ms=excluded.elapsed_ms, started_at=excluded.started_at,
  ended_at=excluded.ended_at, output=excluded.output, steps_json=excluded.steps_json`,
		t.TraceId, t.Task, t.Status, t.Route, t.ElapsedMs, t.StartedAt, t.EndedAt, t.Output, stepsJSON)
	if err != nil {
		log.Printf("[tracestore] save error: %v", err)
		return &tracev1.SaveReply{Ok: false}, nil
	}
	return &tracev1.SaveReply{Ok: true}, nil
}

func (s *TraceStoreServiceImpl) Get(ctx context.Context, req *tracev1.GetRequest) (*tracev1.GetReply, error) {
	t := &tracev1.Trace{}
	var stepsJSON string
	err := s.db.QueryRowContext(ctx, `
SELECT trace_id, task, status, route, elapsed_ms, started_at, ended_at, output, steps_json
FROM traces WHERE trace_id = ?`, req.TraceId).Scan(
		&t.TraceId, &t.Task, &t.Status, &t.Route, &t.ElapsedMs, &t.StartedAt, &t.EndedAt, &t.Output, &stepsJSON)
	if err == sql.ErrNoRows {
		return &tracev1.GetReply{Found: false}, nil
	}
	if err != nil {
		log.Printf("[tracestore] get error: %v", err)
		return &tracev1.GetReply{Found: false}, nil
	}
	t.Steps = unmarshalSteps(stepsJSON)
	return &tracev1.GetReply{Trace: t, Found: true}, nil
}

// List 返回最近的 trace（按 ended_at 倒序），省略 steps 以减负。
func (s *TraceStoreServiceImpl) List(ctx context.Context, req *tracev1.ListRequest) (*tracev1.ListReply, error) {
	limit := int(req.Limit)
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT trace_id, task, status, route, elapsed_ms, started_at, ended_at, output
FROM traces ORDER BY ended_at DESC LIMIT ?`, limit)
	if err != nil {
		log.Printf("[tracestore] list error: %v", err)
		return &tracev1.ListReply{}, nil
	}
	defer rows.Close()
	result := make([]*tracev1.Trace, 0, limit)
	for rows.Next() {
		t := &tracev1.Trace{}
		if err := rows.Scan(&t.TraceId, &t.Task, &t.Status, &t.Route,
			&t.ElapsedMs, &t.StartedAt, &t.EndedAt, &t.Output); err != nil {
			log.Printf("[tracestore] list scan error: %v", err)
			continue
		}
		result = append(result, t) // Steps 留空
	}
	return &tracev1.ListReply{Traces: result}, nil
}

// Close 关闭数据库连接。服务关停时调用。
func (s *TraceStoreServiceImpl) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}
