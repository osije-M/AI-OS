package conf

import "testing"

func TestLoadDefaults(t *testing.T) {
	t.Setenv("TRACE_STORE_ADDR", "")
	t.Setenv("TRACE_STORE_FILE", "")
	t.Setenv("TRACE_STORE_MAX_OUTPUT", "")

	cfg := Load()

	if cfg.GRPCAddr != "0.0.0.0:9400" {
		t.Errorf("GRPCAddr = %q, want %q", cfg.GRPCAddr, "0.0.0.0:9400")
	}
	if cfg.StoreFile != "data/traces.jsonl" {
		t.Errorf("StoreFile = %q, want %q", cfg.StoreFile, "data/traces.jsonl")
	}
	if cfg.MaxOutput != 4096 {
		t.Errorf("MaxOutput = %d, want 4096", cfg.MaxOutput)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("TRACE_STORE_ADDR", "0.0.0.0:19400")
	t.Setenv("TRACE_STORE_FILE", "/custom/traces.jsonl")
	t.Setenv("TRACE_STORE_MAX_OUTPUT", "8192")

	cfg := Load()

	if cfg.GRPCAddr != "0.0.0.0:19400" {
		t.Errorf("GRPCAddr = %q, want %q", cfg.GRPCAddr, "0.0.0.0:19400")
	}
	if cfg.StoreFile != "/custom/traces.jsonl" {
		t.Errorf("StoreFile = %q, want %q", cfg.StoreFile, "/custom/traces.jsonl")
	}
	if cfg.MaxOutput != 8192 {
		t.Errorf("MaxOutput = %d, want 8192", cfg.MaxOutput)
	}
}
