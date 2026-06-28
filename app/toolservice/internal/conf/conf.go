package conf

import (
	"os"
	"strings"
)

// Config holds ToolService configuration loaded from environment variables.
type Config struct {
	GRPCAddr      string
	ExternalTools []string
}

func Load() *Config {
	addr := os.Getenv("TOOL_SERVICE_ADDR")
	if addr == "" {
		addr = "0.0.0.0:9200"
	}

	var externalTools []string
	raw := os.Getenv("EXTERNAL_TOOLS")
	if raw != "" {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				externalTools = append(externalTools, part)
			}
		}
	}

	return &Config{
		GRPCAddr:      addr,
		ExternalTools: externalTools,
	}
}
