package main

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestComputeSummary(t *testing.T) {
	results := []Result{
		{ExpectRoute: "research", ActualRoute: "research", ExpectStatus: "OK", ActualStatus: "OK", LatencyMs: 100},   // pass
		{ExpectRoute: "coding", ActualRoute: "review", ExpectStatus: "OK", ActualStatus: "OK", LatencyMs: 200},       // route miss
		{ExpectRoute: "", ActualRoute: "", ExpectStatus: "DENIED", ActualStatus: "DENIED", LatencyMs: 60},            // deny pass (no route)
		{ExpectRoute: "review", ActualRoute: "review", ExpectStatus: "OK", ActualStatus: "FAILED", LatencyMs: 300},   // status miss
	}
	s := computeSummary(results)
	if s.Total != 4 {
		t.Fatalf("total: got %d want 4", s.Total)
	}
	if s.RouteExpected != 3 { // 三条有 expect_route
		t.Fatalf("routeExpected: got %d want 3", s.RouteExpected)
	}
	if s.RouteMatched != 2 { // research, review 匹配
		t.Fatalf("routeMatched: got %d want 2", s.RouteMatched)
	}
	if s.StatusMatched != 3 { // 除 status miss 那条
		t.Fatalf("statusMatched: got %d want 3", s.StatusMatched)
	}
	if s.Passed != 2 { // research + deny
		t.Fatalf("passed: got %d want 2", s.Passed)
	}
	if s.RouteAcc < 0.66 || s.RouteAcc > 0.67 {
		t.Fatalf("routeAcc: got %.4f want ~0.6667", s.RouteAcc)
	}
	if s.AvgLatencyMs != 165 { // (100+200+60+300)/4
		t.Fatalf("avgLatency: got %.1f want 165", s.AvgLatencyMs)
	}
}

func TestStoreSaveAndQuery(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "eval.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	results := []Result{
		{TaskID: "t1", Task: "a", ExpectRoute: "research", ActualRoute: "research", ExpectStatus: "OK", ActualStatus: "OK", LatencyMs: 100, TraceID: "trace-1"},
		{TaskID: "t2", Task: "b", ExpectRoute: "", ExpectStatus: "DENIED", ActualStatus: "DENIED", LatencyMs: 50},
	}
	sum := computeSummary(results)
	meta := RunMeta{RunID: "run-1", Suite: "eval/suite.yaml", GitSHA: "abc123", StartedAt: 1000, EndedAt: 2000}
	if err := store.SaveRun(meta, sum, results); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}

	// 独立连接查回，验证落库与可聚合查询
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	var runID string
	var total, passed int
	var routeAcc float64
	if err := db.QueryRow("SELECT run_id, total, passed, route_acc FROM eval_runs").Scan(&runID, &total, &passed, &routeAcc); err != nil {
		t.Fatalf("query run: %v", err)
	}
	if runID != "run-1" || total != 2 || passed != 2 {
		t.Fatalf("run row mismatch: id=%s total=%d passed=%d", runID, total, passed)
	}

	var nResults int
	if err := db.QueryRow("SELECT COUNT(*) FROM eval_results WHERE run_id = ?", "run-1").Scan(&nResults); err != nil {
		t.Fatalf("count results: %v", err)
	}
	if nResults != 2 {
		t.Fatalf("results count: got %d want 2", nResults)
	}

	// route_match 记法：t1=1(匹配)，t2=-1(不适用)
	var rm int
	if err := db.QueryRow("SELECT route_match FROM eval_results WHERE run_id=? AND task_id=?", "run-1", "t2").Scan(&rm); err != nil {
		t.Fatalf("query route_match: %v", err)
	}
	if rm != -1 {
		t.Fatalf("t2 route_match: got %d want -1 (n/a)", rm)
	}
}
