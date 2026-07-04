package conf

import "testing"

func TestLoadDefaults(t *testing.T) {
	t.Setenv("GATEWAY_HTTP_ADDR", "")
	t.Setenv("ORCHESTRATOR_ADDR", "")
	t.Setenv("TRACE_STORE_ADDR", "")

	cfg := Load()

	if cfg.HTTPAddr != "0.0.0.0:8000" {
		t.Errorf("HTTPAddr = %q, want %q", cfg.HTTPAddr, "0.0.0.0:8000")
	}
	if cfg.OrchestratorAddr != "127.0.0.1:9300" {
		t.Errorf("OrchestratorAddr = %q, want %q", cfg.OrchestratorAddr, "127.0.0.1:9300")
	}
	if cfg.TraceStoreAddr != "127.0.0.1:9400" {
		t.Errorf("TraceStoreAddr = %q, want %q", cfg.TraceStoreAddr, "127.0.0.1:9400")
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("GATEWAY_HTTP_ADDR", "0.0.0.0:18000")
	t.Setenv("ORCHESTRATOR_ADDR", "orchestrator:9300")
	t.Setenv("TRACE_STORE_ADDR", "tracestore:9400")

	cfg := Load()

	if cfg.HTTPAddr != "0.0.0.0:18000" {
		t.Errorf("HTTPAddr = %q, want %q", cfg.HTTPAddr, "0.0.0.0:18000")
	}
	if cfg.OrchestratorAddr != "orchestrator:9300" {
		t.Errorf("OrchestratorAddr = %q, want %q", cfg.OrchestratorAddr, "orchestrator:9300")
	}
	if cfg.TraceStoreAddr != "tracestore:9400" {
		t.Errorf("TraceStoreAddr = %q, want %q", cfg.TraceStoreAddr, "tracestore:9400")
	}
}
