package conf

import (
	"reflect"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("TOOL_SERVICE_ADDR", "")
	t.Setenv("EXTERNAL_TOOLS", "")

	cfg := Load()

	if cfg.GRPCAddr != "0.0.0.0:9200" {
		t.Errorf("GRPCAddr = %q, want %q", cfg.GRPCAddr, "0.0.0.0:9200")
	}
	if len(cfg.ExternalTools) != 0 {
		t.Errorf("ExternalTools = %v, want empty/nil", cfg.ExternalTools)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("TOOL_SERVICE_ADDR", "0.0.0.0:19200")
	t.Setenv("EXTERNAL_TOOLS", " http://a , http://b ")

	cfg := Load()

	if cfg.GRPCAddr != "0.0.0.0:19200" {
		t.Errorf("GRPCAddr = %q, want %q", cfg.GRPCAddr, "0.0.0.0:19200")
	}
	want := []string{"http://a", "http://b"}
	if !reflect.DeepEqual(cfg.ExternalTools, want) {
		t.Errorf("ExternalTools = %v, want %v", cfg.ExternalTools, want)
	}
}
