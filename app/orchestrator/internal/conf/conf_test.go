package conf

import "testing"

func TestLoadDefaults(t *testing.T) {
	// Ensure none of the relevant env vars are set.
	for _, k := range []string{
		"ORCHESTRATOR_ADDR", "AGENT_RUNTIME_ADDR", "ORCH_MAX_RETRIES",
		"ORCH_RETRY_BACKOFF_MS", "TRACE_STORE_ADDR", "POLICY_FILE",
	} {
		t.Setenv(k, "")
	}

	cfg := Load()

	if cfg.GRPCAddr != "0.0.0.0:9300" {
		t.Errorf("GRPCAddr = %q, want %q", cfg.GRPCAddr, "0.0.0.0:9300")
	}
	if cfg.AgentRuntimeAddr != "127.0.0.1:9100" {
		t.Errorf("AgentRuntimeAddr = %q, want %q", cfg.AgentRuntimeAddr, "127.0.0.1:9100")
	}
	if cfg.MaxRetries != 2 {
		t.Errorf("MaxRetries = %d, want 2", cfg.MaxRetries)
	}
	if cfg.BackoffMs != 300 {
		t.Errorf("BackoffMs = %d, want 300", cfg.BackoffMs)
	}
	if cfg.TraceStoreAddr != "127.0.0.1:9400" {
		t.Errorf("TraceStoreAddr = %q, want %q", cfg.TraceStoreAddr, "127.0.0.1:9400")
	}
	if cfg.PolicyFile != "configs/policy.yaml" {
		t.Errorf("PolicyFile = %q, want %q", cfg.PolicyFile, "configs/policy.yaml")
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("ORCHESTRATOR_ADDR", "0.0.0.0:19300")
	t.Setenv("AGENT_RUNTIME_ADDR", "agentruntime:9100")
	t.Setenv("ORCH_MAX_RETRIES", "5")
	t.Setenv("ORCH_RETRY_BACKOFF_MS", "1000")
	t.Setenv("TRACE_STORE_ADDR", "tracestore:9400")
	t.Setenv("POLICY_FILE", "/custom/policy.yaml")

	cfg := Load()

	if cfg.GRPCAddr != "0.0.0.0:19300" {
		t.Errorf("GRPCAddr = %q, want %q", cfg.GRPCAddr, "0.0.0.0:19300")
	}
	if cfg.AgentRuntimeAddr != "agentruntime:9100" {
		t.Errorf("AgentRuntimeAddr = %q, want %q", cfg.AgentRuntimeAddr, "agentruntime:9100")
	}
	if cfg.MaxRetries != 5 {
		t.Errorf("MaxRetries = %d, want 5", cfg.MaxRetries)
	}
	if cfg.BackoffMs != 1000 {
		t.Errorf("BackoffMs = %d, want 1000", cfg.BackoffMs)
	}
	if cfg.TraceStoreAddr != "tracestore:9400" {
		t.Errorf("TraceStoreAddr = %q, want %q", cfg.TraceStoreAddr, "tracestore:9400")
	}
	if cfg.PolicyFile != "/custom/policy.yaml" {
		t.Errorf("PolicyFile = %q, want %q", cfg.PolicyFile, "/custom/policy.yaml")
	}
}
