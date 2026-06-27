package conf

import (
	"os"
	"strconv"
)

type Config struct {
	GRPCAddr         string
	AgentRuntimeAddr string
	MaxRetries       int // ORCH_MAX_RETRIES，默认 2
	BackoffMs        int // ORCH_RETRY_BACKOFF_MS，默认 300
	TraceStoreAddr   string
}

func Load() *Config {
	addr := os.Getenv("ORCHESTRATOR_ADDR")
	if addr == "" {
		addr = "0.0.0.0:9300"
	}
	agentAddr := os.Getenv("AGENT_RUNTIME_ADDR")
	if agentAddr == "" {
		agentAddr = "127.0.0.1:9100"
	}

	maxRetries := 2
	if v, err := strconv.Atoi(os.Getenv("ORCH_MAX_RETRIES")); err == nil {
		maxRetries = v
	}

	backoffMs := 300
	if v, err := strconv.Atoi(os.Getenv("ORCH_RETRY_BACKOFF_MS")); err == nil {
		backoffMs = v
	}

	traceStoreAddr := os.Getenv("TRACE_STORE_ADDR")
	if traceStoreAddr == "" {
		traceStoreAddr = "127.0.0.1:9400"
	}

	return &Config{
		GRPCAddr:         addr,
		AgentRuntimeAddr: agentAddr,
		MaxRetries:       maxRetries,
		BackoffMs:        backoffMs,
		TraceStoreAddr:   traceStoreAddr,
	}
}
