package conf

import "os"

// Config holds ToolService configuration loaded from environment variables.
type Config struct {
	GRPCAddr string
}

func Load() *Config {
	addr := os.Getenv("TOOL_SERVICE_ADDR")
	if addr == "" {
		addr = "0.0.0.0:9200"
	}
	return &Config{
		GRPCAddr: addr,
	}
}
