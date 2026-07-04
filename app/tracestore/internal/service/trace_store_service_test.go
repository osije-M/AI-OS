package service

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	tracev1 "github.com/osije/ai-os/api/gen/go/trace/v1"
	"github.com/osije/ai-os/app/tracestore/internal/conf"
	_ "modernc.org/sqlite"
)

func TestSaveGetReplay(t *testing.T) {
	tmpDir := t.TempDir()
	storeFile := tmpDir + "/traces.db"

	cfg := &conf.Config{
		GRPCAddr:  "0.0.0.0:19400",
		StoreFile: storeFile,
		MaxOutput: 4096,
	}

	ctx := context.Background()
	traceID := "smoke-trace-001"

	// === Round 1: Save + Get ===
	svc, err := NewTraceStoreServiceImpl(cfg)
	if err != nil {
		t.Fatalf("NewTraceStoreServiceImpl: %v", err)
	}

	saveReply, err := svc.Save(ctx, &tracev1.SaveRequest{
		Trace: &tracev1.Trace{
			TraceId:   traceID,
			Task:      "smoke test task",
			Status:    "OK",
			Route:     "coding",
			ElapsedMs: 1234,
			StartedAt: 1000000,
			EndedAt:   1001234,
			Output:    "hello from smoke test",
			Steps: []*tracev1.Step{
				{Seq: 1, Node: "supervisor", Type: "control", Summary: "routed -> coding", LatencyMs: 300, Status: "ok"},
				{Seq: 2, Node: "coding", Type: "llm", Summary: "via deepseek-chat", LatencyMs: 900, Status: "ok"},
			},
		},
	})
	if err != nil || !saveReply.Ok {
		t.Fatalf("Save failed: err=%v, ok=%v", err, saveReply)
	}
	fmt.Printf("[PASS] Save returned Ok=true\n")

	getReply, err := svc.Get(ctx, &tracev1.GetRequest{TraceId: traceID})
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if !getReply.Found {
		t.Fatalf("Get: trace not found after Save")
	}
	fmt.Printf("[PASS] Get: found trace id=%s task=%q status=%s route=%s steps=%d\n",
		getReply.Trace.TraceId, getReply.Trace.Task, getReply.Trace.Status,
		getReply.Trace.Route, len(getReply.Trace.Steps))

	// Verify SQLite DB file was created
	if fi, statErr := os.Stat(storeFile); statErr != nil || fi.Size() == 0 {
		t.Fatalf("SQLite DB file missing/empty after Save: err=%v", statErr)
	}
	fmt.Printf("[PASS] SQLite DB file created\n")

	// === Round 2: Simulate restart — new service instance, same JSONL file ===
	fmt.Printf("\n--- Simulating restart (new instance, same JSONL file) ---\n")
	svc2, err := NewTraceStoreServiceImpl(cfg)
	if err != nil {
		t.Fatalf("NewTraceStoreServiceImpl (restart): %v", err)
	}

	getReply2, err := svc2.Get(ctx, &tracev1.GetRequest{TraceId: traceID})
	if err != nil {
		t.Fatalf("Get (after restart) error: %v", err)
	}
	if !getReply2.Found {
		t.Fatalf("Get (after restart): trace NOT found — JSONL replay failed")
	}
	fmt.Printf("[PASS] After restart: found trace id=%s task=%q status=%s route=%s steps=%d\n",
		getReply2.Trace.TraceId, getReply2.Trace.Task, getReply2.Trace.Status,
		getReply2.Trace.Route, len(getReply2.Trace.Steps))

	// === Round 3: List ===
	listReply, err := svc2.List(ctx, &tracev1.ListRequest{Limit: 10})
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(listReply.Traces) == 0 {
		t.Fatalf("List returned 0 traces")
	}
	first := listReply.Traces[0]
	fmt.Printf("[PASS] List returned %d trace(s), first: traceId=%s task=%q steps_in_list=%d (should be 0, omitted)\n",
		len(listReply.Traces), first.TraceId, first.Task, len(first.Steps))
	if len(first.Steps) != 0 {
		t.Errorf("List should omit steps, but got %d steps", len(first.Steps))
	}

	fmt.Println("\nAll smoke tests PASSED.")

	// Close file handles so TempDir can clean up
	svc.Close()
	svc2.Close()
}

// TestListStatsQueryable 证明换 SQLite 后可直接对持久化数据跑聚合统计
// （这是从 JSONL 换成 SQLite 的核心价值，也为后续 Eval 框架打基础）。
func TestListStatsQueryable(t *testing.T) {
	tmpDir := t.TempDir()
	storeFile := tmpDir + "/traces.db"
	cfg := &conf.Config{GRPCAddr: "0.0.0.0:19401", StoreFile: storeFile, MaxOutput: 4096}
	ctx := context.Background()

	svc, err := NewTraceStoreServiceImpl(cfg)
	if err != nil {
		t.Fatalf("NewTraceStoreServiceImpl: %v", err)
	}
	defer svc.Close()

	seed := []*tracev1.Trace{
		{TraceId: "t1", Status: "OK", Route: "coding", EndedAt: 100},
		{TraceId: "t2", Status: "OK", Route: "research", EndedAt: 200},
		{TraceId: "t3", Status: "FAILED", Route: "audit", EndedAt: 300},
	}
	for _, tr := range seed {
		reply, err := svc.Save(ctx, &tracev1.SaveRequest{Trace: tr})
		if err != nil || !reply.Ok {
			t.Fatalf("Save(%s) failed: err=%v ok=%v", tr.TraceId, err, reply)
		}
	}

	// 直接对同一个库跑聚合查询，验证按 status 统计
	db, err := sql.Open("sqlite", storeFile)
	if err != nil {
		t.Fatalf("open db for stats: %v", err)
	}
	defer db.Close()

	counts := map[string]int{}
	rows, err := db.QueryContext(ctx, "SELECT status, COUNT(*) FROM traces GROUP BY status")
	if err != nil {
		t.Fatalf("stats query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		counts[status] = n
	}
	if counts["OK"] != 2 || counts["FAILED"] != 1 {
		t.Fatalf("stats mismatch: got %v, want OK=2 FAILED=1", counts)
	}

	// 成功率统计（Eval 框架会用到的那类聚合）
	var total, ok int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*), SUM(CASE WHEN status='OK' THEN 1 ELSE 0 END) FROM traces").Scan(&total, &ok)
	if total != 3 || ok != 2 {
		t.Fatalf("success-rate agg mismatch: total=%d ok=%d, want 3/2", total, ok)
	}
	fmt.Printf("[PASS] SQL stats over persisted traces: %v, success=%d/%d\n", counts, ok, total)
}
