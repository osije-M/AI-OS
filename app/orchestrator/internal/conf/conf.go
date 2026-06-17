package conf

import "os"

type Config struct {
	GRPCAddr         string
	AgentRuntimeAddr string
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
	return &Config{
		GRPCAddr:         addr,
		AgentRuntimeAddr: agentAddr,
	}
}
