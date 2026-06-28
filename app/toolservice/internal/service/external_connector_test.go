package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	toolv1 "github.com/osije/ai-os/api/gen/go/tool/v1"
	"github.com/osije/ai-os/app/toolservice/internal/conf"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// newMockServer starts a fake external tool HTTP server.
// GET /spec → spec JSON.
// POST /invoke → ok=true with fixed output.
func newMockServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/spec", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"name":          "mocktool",
			"description":   "测试用 mock 工具",
			"input_schema":  "{}",
			"output_schema": "{}",
		})
	})
	mux.HandleFunc("/invoke", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":          true,
			"output_json": `{"echo":"from_mock"}`,
			"error":       "",
		})
	})
	return httptest.NewServer(mux)
}

// TestExternalListToolsIncludesMocktool verifies that ListTools returns echo, reverse, and mocktool
// when a healthy external endpoint is configured.
func TestExternalListToolsIncludesMocktool(t *testing.T) {
	srv := newMockServer(t)
	defer srv.Close()

	cfg := &conf.Config{
		GRPCAddr:      "0.0.0.0:0",
		ExternalTools: []string{srv.URL},
	}
	impl := NewToolServiceImpl(cfg)

	reply, err := impl.ListTools(context.Background(), &toolv1.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools error: %v", err)
	}

	nameSet := make(map[string]bool)
	for _, tool := range reply.Tools {
		nameSet[tool.Name] = true
	}

	for _, expected := range []string{"echo", "reverse", "mocktool"} {
		if !nameSet[expected] {
			t.Errorf("expected tool %q in ListTools result, full set: %v", expected, nameSet)
		}
	}
	t.Logf("ListTools returned %d tools: %v", len(reply.Tools), nameSet)
}

// TestExternalInvokeMocktool verifies that Invoke("mocktool") proxies to the external server and returns ok=true.
func TestExternalInvokeMocktool(t *testing.T) {
	srv := newMockServer(t)
	defer srv.Close()

	cfg := &conf.Config{
		GRPCAddr:      "0.0.0.0:0",
		ExternalTools: []string{srv.URL},
	}
	impl := NewToolServiceImpl(cfg)

	// Populate the externalURLs map via ListTools first.
	if _, err := impl.ListTools(context.Background(), &toolv1.ListToolsRequest{}); err != nil {
		t.Fatalf("ListTools error: %v", err)
	}

	reply, err := impl.Invoke(context.Background(), &toolv1.InvokeRequest{
		Name:      "mocktool",
		InputJson: "{}",
	})
	if err != nil {
		t.Fatalf("Invoke(mocktool) error: %v", err)
	}
	if !reply.Ok {
		t.Errorf("expected ok=true, got ok=false, error=%q", reply.Error)
	}
	t.Logf("Invoke(mocktool) output_json: %s", reply.OutputJson)
}

// TestExternalInvokeEchoBuiltin verifies the built-in echo tool still works.
func TestExternalInvokeEchoBuiltin(t *testing.T) {
	cfg := &conf.Config{GRPCAddr: "0.0.0.0:0"}
	impl := NewToolServiceImpl(cfg)

	input := `{"input":"hi"}`
	reply, err := impl.Invoke(context.Background(), &toolv1.InvokeRequest{
		Name:      "echo",
		InputJson: input,
	})
	if err != nil {
		t.Fatalf("Invoke(echo) error: %v", err)
	}
	if !reply.Ok {
		t.Errorf("expected ok=true, got false")
	}
	if reply.OutputJson != input {
		t.Errorf("expected output_json=%q, got %q", input, reply.OutputJson)
	}
	t.Logf("Invoke(echo) ok, output_json: %s", reply.OutputJson)
}

// TestExternalInvokeUnknownTool verifies that Invoke with an unknown name returns gRPC NotFound.
func TestExternalInvokeUnknownTool(t *testing.T) {
	cfg := &conf.Config{GRPCAddr: "0.0.0.0:0"}
	impl := NewToolServiceImpl(cfg)

	_, err := impl.Invoke(context.Background(), &toolv1.InvokeRequest{
		Name:      "unknown_tool",
		InputJson: "{}",
	})
	if err == nil {
		t.Fatal("expected NotFound error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.NotFound {
		t.Errorf("expected NotFound code, got %v", st.Code())
	}
	t.Logf("Invoke(unknown_tool) correctly returned gRPC NotFound: %v", err)
}

// TestExternalEmptyExternalTools verifies that an empty ExternalTools list yields exactly 2 builtin tools.
func TestExternalEmptyExternalTools(t *testing.T) {
	cfg := &conf.Config{
		GRPCAddr:      "0.0.0.0:0",
		ExternalTools: []string{},
	}
	impl := NewToolServiceImpl(cfg)

	reply, err := impl.ListTools(context.Background(), &toolv1.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools error: %v", err)
	}
	if len(reply.Tools) != 2 {
		t.Errorf("expected 2 builtin tools, got %d", len(reply.Tools))
	}
	t.Logf("Empty ExternalTools: ListTools returned %d tools (expected 2)", len(reply.Tools))
}

// TestExternalDeadURL verifies that a dead external URL causes a warning log but does not fail ListTools.
func TestExternalDeadURL(t *testing.T) {
	cfg := &conf.Config{
		GRPCAddr:      "0.0.0.0:0",
		ExternalTools: []string{"http://127.0.0.1:19999"},
	}
	impl := NewToolServiceImpl(cfg)

	reply, err := impl.ListTools(context.Background(), &toolv1.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools should not error on dead URL, got: %v", err)
	}
	if len(reply.Tools) != 2 {
		t.Errorf("expected 2 builtin tools when external URL is dead, got %d", len(reply.Tools))
	}
	t.Logf("Dead URL: ListTools returned %d tools (expected 2, no crash)", len(reply.Tools))
}
