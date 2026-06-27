package service

import (
	"bufio"
	"context"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	tracev1 "github.com/osije/ai-os/api/gen/go/trace/v1"
	"github.com/osije/ai-os/app/tracestore/internal/conf"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type TraceStoreServiceImpl struct {
	tracev1.UnimplementedTraceStoreServer
	mu         sync.RWMutex
	traces     map[string]*tracev1.Trace
	storeFile  string
	fileHandle *os.File
}

func NewTraceStoreServiceImpl(cfg *conf.Config) (*TraceStoreServiceImpl, error) {
	svc := &TraceStoreServiceImpl{
		traces:    make(map[string]*tracev1.Trace),
		storeFile: cfg.StoreFile,
	}
	if err := svc.initStore(); err != nil {
		return nil, err
	}
	return svc, nil
}

func (s *TraceStoreServiceImpl) initStore() error {
	dir := filepath.Dir(s.storeFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	// 回放已有记录
	if f, err := os.Open(s.storeFile); err == nil {
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			t := &tracev1.Trace{}
			if err2 := protojson.Unmarshal([]byte(line), t); err2 == nil {
				s.traces[t.TraceId] = t
			} else {
				log.Printf("[tracestore] replay parse error: %v", err2)
			}
		}
		f.Close()
		log.Printf("[tracestore] replayed %d traces from %s", len(s.traces), s.storeFile)
	}
	// 打开追加
	fh, err := os.OpenFile(s.storeFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	s.fileHandle = fh
	return nil
}

func (s *TraceStoreServiceImpl) Save(ctx context.Context, req *tracev1.SaveRequest) (*tracev1.SaveReply, error) {
	t := req.Trace
	if t == nil {
		return &tracev1.SaveReply{Ok: false}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.traces[t.TraceId] = t
	// 追加一行 JSONL
	b, err := protojson.Marshal(t)
	if err == nil {
		s.fileHandle.Write(b)
		s.fileHandle.Write([]byte("\n"))
	} else {
		log.Printf("[tracestore] marshal error: %v", err)
	}
	return &tracev1.SaveReply{Ok: true}, nil
}

func (s *TraceStoreServiceImpl) Get(ctx context.Context, req *tracev1.GetRequest) (*tracev1.GetReply, error) {
	s.mu.RLock()
	t, ok := s.traces[req.TraceId]
	s.mu.RUnlock()
	if !ok {
		return &tracev1.GetReply{Found: false}, nil
	}
	return &tracev1.GetReply{Trace: t, Found: true}, nil
}

// Close releases the file handle. Call on service shutdown.
func (s *TraceStoreServiceImpl) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fileHandle != nil {
		return s.fileHandle.Close()
	}
	return nil
}

func (s *TraceStoreServiceImpl) List(ctx context.Context, req *tracev1.ListRequest) (*tracev1.ListReply, error) {
	limit := int(req.Limit)
	if limit <= 0 {
		limit = 20
	}
	s.mu.RLock()
	all := make([]*tracev1.Trace, 0, len(s.traces))
	for _, t := range s.traces {
		all = append(all, t)
	}
	s.mu.RUnlock()
	// 按 ended_at 倒序
	sort.Slice(all, func(i, j int) bool {
		return all[i].EndedAt > all[j].EndedAt
	})
	if len(all) > limit {
		all = all[:limit]
	}
	// List 时省略 steps（减负）
	result := make([]*tracev1.Trace, 0, len(all))
	for _, t := range all {
		summary := proto.Clone(t).(*tracev1.Trace)
		summary.Steps = nil
		result = append(result, summary)
	}
	return &tracev1.ListReply{Traces: result}, nil
}
