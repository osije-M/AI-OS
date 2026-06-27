package conf

import (
	"os"
	"strconv"
)

type Config struct {
	GRPCAddr  string
	StoreFile string
	MaxOutput int
}

func Load() *Config {
	addr := os.Getenv("TRACE_STORE_ADDR")
	if addr == "" {
		addr = "0.0.0.0:9400"
	}
	file := os.Getenv("TRACE_STORE_FILE")
	if file == "" {
		file = "data/traces.jsonl"
	}
	maxOutput := 4096
	if v := os.Getenv("TRACE_STORE_MAX_OUTPUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			maxOutput = n
		}
	}
	return &Config{
		GRPCAddr:  addr,
		StoreFile: file,
		MaxOutput: maxOutput,
	}
}
