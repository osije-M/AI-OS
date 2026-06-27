package service

import (
	"context"
	"fmt"
	"os"
	"testing"

	tracev1 "github.com/osije/ai-os/api/gen/go/trace/v1"
	"github.com/osije/ai-os/app/tracestore/internal/conf"
)

func TestSaveGetReplay(t *testing.T) {
	tmpDir := t.TempDir()
	storeFile := tmpDir + "/traces.jsonl"

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

	// Verify JSONL file was written
	content, _ := os.ReadFile(storeFile)
	if len(content) == 0 {
		t.Fatalf("JSONL file is empty after Save")
	}
	fmt.Printf("[PASS] JSONL file written (%d bytes)\n", len(content))

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
