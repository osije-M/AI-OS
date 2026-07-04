package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动
)

// Store 是独立的评测结果库（与运行时 trace 库解耦）。
type Store struct{ db *sql.DB }

// 规范化两张表：eval_runs（一次评测的汇总）+ eval_results（逐用例明细，含 trace_id
// 便于回运行时 trace 库下钻）。标量指标列可直接 SQL 聚合、对比历史 run。
const evalSchema = `
CREATE TABLE IF NOT EXISTS eval_runs (
  run_id         TEXT PRIMARY KEY,
  suite          TEXT,
  git_sha        TEXT,
  started_at     INTEGER,   -- unix 毫秒
  ended_at       INTEGER,
  total          INTEGER,
  passed         INTEGER,
  route_acc      REAL,      -- 路由准确率 [0,1]
  success_rate   REAL,      -- 状态匹配率 [0,1]
  avg_latency_ms REAL
);
CREATE TABLE IF NOT EXISTS eval_results (
  run_id        TEXT NOT NULL,
  task_id       TEXT NOT NULL,
  task          TEXT,
  expect_route  TEXT,
  actual_route  TEXT,
  route_match   INTEGER,    -- 0/1；expect_route 为空时记 -1(不适用)
  expect_status TEXT,
  actual_status TEXT,
  status_match  INTEGER,    -- 0/1
  latency_ms    INTEGER,
  trace_id      TEXT,
  err           TEXT,
  PRIMARY KEY (run_id, task_id)
);
CREATE INDEX IF NOT EXISTS idx_eval_results_run     ON eval_results(run_id);
CREATE INDEX IF NOT EXISTS idx_eval_runs_started_at ON eval_runs(started_at);
`

// RunMeta 是一次评测的元信息。
type RunMeta struct {
	RunID     string
	Suite     string
	GitSHA    string
	StartedAt int64
	EndedAt   int64
}

func OpenStore(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", path))
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(evalSchema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// SaveRun 在一个事务里写入本次 run 的汇总与逐用例结果。
func (s *Store) SaveRun(meta RunMeta, sum Summary, results []Result) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck  // Commit 成功后是 no-op

	if _, err := tx.Exec(`
INSERT INTO eval_runs (run_id, suite, git_sha, started_at, ended_at, total, passed, route_acc, success_rate, avg_latency_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		meta.RunID, meta.Suite, meta.GitSHA, meta.StartedAt, meta.EndedAt,
		sum.Total, sum.Passed, sum.RouteAcc, sum.SuccessRate, sum.AvgLatencyMs); err != nil {
		return err
	}

	stmt, err := tx.Prepare(`
INSERT INTO eval_results (run_id, task_id, task, expect_route, actual_route, route_match,
  expect_status, actual_status, status_match, latency_ms, trace_id, err)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range results {
		routeMatch := -1 // 不适用（expect_route 为空）
		if r.ExpectRoute != "" {
			if r.ActualRoute == r.ExpectRoute {
				routeMatch = 1
			} else {
				routeMatch = 0
			}
		}
		statusMatch := 0
		if r.statusMatched() {
			statusMatch = 1
		}
		if _, err := stmt.Exec(meta.RunID, r.TaskID, r.Task, r.ExpectRoute, r.ActualRoute, routeMatch,
			r.ExpectStatus, r.ActualStatus, statusMatch, r.LatencyMs, r.TraceID, r.Err); err != nil {
			return err
		}
	}
	return tx.Commit()
}
