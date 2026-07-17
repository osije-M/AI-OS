package runs

import (
	"fmt"
	"sync"
	"testing"
	"time"

	orchestratorv1 "github.com/osije/ai-os/api/gen/go/orchestrator/v1"
)

const (
	running   = orchestratorv1.RunState_RUN_STATE_RUNNING
	done      = orchestratorv1.RunState_RUN_STATE_DONE
	failed    = orchestratorv1.RunState_RUN_STATE_FAILED
	cancelled = orchestratorv1.RunState_RUN_STATE_CANCELLED
)

func find(list []*orchestratorv1.RunInfo, id string) *orchestratorv1.RunInfo {
	for _, r := range list {
		if r.TraceId == id {
			return r
		}
	}
	return nil
}

func TestLifecycleAndTransitions(t *testing.T) {
	r := New(time.Minute, 10, nil)
	r.Register("t1", "task one")

	if got := find(r.List(), "t1"); got == nil || got.State != running {
		t.Fatalf("expect t1 RUNNING, got %+v", got)
	}

	r.MarkNode("t1", "research")
	r.SetRoute("t1", "research")
	r.Finish("t1", done, "")

	got := find(r.List(), "t1")
	if got.State != done || got.Route != "research" || got.CurrentNode != "research" {
		t.Fatalf("unexpected after finish: %+v", got)
	}

	// 终态单调:再次 Finish 被忽略
	r.Finish("t1", failed, "")
	if got := find(r.List(), "t1"); got.State != done {
		t.Fatalf("terminal state must be immutable, got %v", got.State)
	}
	// 终态后 MarkNode/Touch/SetRoute 均忽略
	r.MarkNode("t1", "late")
	r.SetRoute("t1", "late")
	if got := find(r.List(), "t1"); got.CurrentNode == "late" || got.Route == "late" {
		t.Fatalf("mutation after terminal must be ignored: %+v", got)
	}
}

func TestFinishIgnoresInvalid(t *testing.T) {
	r := New(time.Minute, 10, nil)
	r.Finish("nope", done, "")               // unknown trace:不 panic
	r.Register("t1", "x")
	r.Finish("t1", running, "")              // 非终态参数:忽略
	if got := find(r.List(), "t1"); got.State != running {
		t.Fatalf("invalid Finish must be ignored, got %v", got.State)
	}
}

func TestTTLEviction(t *testing.T) {
	r := New(50*time.Millisecond, 10, nil)
	base := time.Now()
	r.now = func() time.Time { return base }
	r.Register("t1", "x")
	r.Finish("t1", done, "")

	r.now = func() time.Time { return base.Add(100 * time.Millisecond) }
	if got := find(r.List(), "t1"); got != nil {
		t.Fatalf("terminal run should be TTL-evicted, still present: %+v", got)
	}
}

func TestCapacityPrefersTerminalEviction(t *testing.T) {
	r := New(time.Hour, 3, nil)
	r.Register("old-done", "x")
	r.Finish("old-done", done, "")
	r.Register("r1", "x")
	r.Register("r2", "x")
	// 第 4 个触发驱逐:应优先踢终态 old-done,而不是 RUNNING
	r.Register("r3", "x")

	list := r.List()
	if find(list, "old-done") != nil {
		t.Fatalf("terminal run should be evicted first")
	}
	for _, id := range []string{"r1", "r2", "r3"} {
		if find(list, id) == nil {
			t.Fatalf("running run %s must survive eviction", id)
		}
	}
}

func TestActiveCountAndGaugeHook(t *testing.T) {
	var mu sync.Mutex
	last := -1
	r := New(time.Minute, 10, func(n int) { mu.Lock(); last = n; mu.Unlock() })

	r.Register("t1", "x")
	r.Register("t2", "x")
	mu.Lock()
	if last != 2 {
		t.Fatalf("gauge hook expect 2, got %d", last)
	}
	mu.Unlock()

	r.Finish("t1", cancelled, "")
	mu.Lock()
	defer mu.Unlock()
	if last != 1 || r.ActiveCount() != 1 {
		t.Fatalf("gauge hook expect 1, got hook=%d active=%d", last, r.ActiveCount())
	}
}

func TestConcurrentAccess(t *testing.T) {
	r := New(time.Minute, 500, nil)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("t%d", i)
			r.Register(id, "task")
			r.MarkNode(id, "n")
			r.Touch(id)
			r.List()
			r.Finish(id, done, "route")
		}(i)
	}
	wg.Wait()
	for _, run := range r.List() {
		if run.State != done {
			t.Fatalf("all runs should be DONE, got %v for %s", run.State, run.TraceId)
		}
	}
}

func TestTaskPreviewTruncation(t *testing.T) {
	long := ""
	for i := 0; i < 100; i++ {
		long += "字"
	}
	r := New(time.Minute, 10, nil)
	r.Register("t1", long)
	got := find(r.List(), "t1")
	if l := len([]rune(got.TaskPreview)); l != taskPreviewMaxRunes+1 { // +1 省略号
		t.Fatalf("preview should be %d runes, got %d", taskPreviewMaxRunes+1, l)
	}
}
