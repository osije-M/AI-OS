package policy

import (
	"strings"
	"testing"
)

func testEngine() *Engine {
	return &Engine{
		rules: []Rule{
			{
				Name:          "block-secrets",
				Action:        "deny",
				MatchKeywords: []string{"rm -rf", "drop table", "私钥", "信用卡号"},
				Reason:        "命中高危关键词",
			},
			{
				Name:         "truncate-overlong",
				Action:       "transform",
				MaxTaskChars: 4000,
			},
		},
	}
}

func TestDeny(t *testing.T) {
	e := testEngine()
	task := "please drop table users"
	d := e.Evaluate(task)
	if d.Action != "deny" {
		t.Fatalf("expected deny, got %q", d.Action)
	}
	if d.Reason == "" {
		t.Fatal("expected non-empty reason for deny")
	}
	t.Logf("Decision: action=%s reason=%s task=%q", d.Action, d.Reason, d.Task)
}

func TestTransform(t *testing.T) {
	e := testEngine()
	task := strings.Repeat("a", 5000)
	d := e.Evaluate(task)
	if d.Action != "transform" {
		t.Fatalf("expected transform, got %q", d.Action)
	}
	if len(d.Task) != 4000 {
		t.Fatalf("expected task truncated to 4000 chars, got %d", len(d.Task))
	}
	t.Logf("Decision: action=%s reason=%s taskLen=%d", d.Action, d.Reason, len(d.Task))
}

func TestAllow(t *testing.T) {
	e := testEngine()
	task := "summarize this document"
	d := e.Evaluate(task)
	if d.Action != "allow" {
		t.Fatalf("expected allow, got %q", d.Action)
	}
	if d.Task != task {
		t.Fatalf("expected task unchanged, got %q", d.Task)
	}
	t.Logf("Decision: action=%s task=%q", d.Action, d.Task)
}

func TestLoad(t *testing.T) {
	// Test that Load with a bad path returns empty engine (safe degradation), not error.
	e, err := Load("nonexistent_file.yaml")
	if err != nil {
		t.Fatalf("expected nil error on missing file, got: %v", err)
	}
	if e == nil {
		t.Fatal("expected non-nil engine on missing file")
	}
	// Empty engine should allow everything.
	d := e.Evaluate("drop table users")
	if d.Action != "allow" {
		t.Fatalf("empty engine should allow all, got %q", d.Action)
	}
	t.Logf("Empty engine decision: action=%s", d.Action)
}
