package conf

import (
	"os"
)

type Config struct {
	HTTPAddr         string
	OrchestratorAddr string
	TraceStoreAddr   string
}

func Load() *Config {
	httpAddr := os.Getenv("GATEWAY_HTTP_ADDR")
	if httpAddr == "" {
		httpAddr = "0.0.0.0:8000"
	}
	orchAddr := os.Getenv("ORCHESTRATOR_ADDR")
	if orchAddr == "" {
		orchAddr = "127.0.0.1:9300"
	}
	traceStoreAddr := os.Getenv("TRACE_STORE_ADDR")
	if traceStoreAddr == "" {
		traceStoreAddr = "127.0.0.1:9400"
	}
	return &Config{
		HTTPAddr:         httpAddr,
		OrchestratorAddr: orchAddr,
		TraceStoreAddr:   traceStoreAddr,
	}
}
