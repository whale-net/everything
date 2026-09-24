package main

import "testing"

// TestLoadConfig_MCPPublicURL covers issue #2645's ASS_WEB_MCP_RESOURCE_URL
// override: unset, it falls back to ASS_MCP_PUBLIC_URL verbatim (backward
// compatible); set, it wins over ASS_MCP_PUBLIC_URL so `web` can satisfy
// auth's loopback/https validation in local dev without touching `mcp`'s
// in-cluster value.
func TestLoadConfig_MCPPublicURL(t *testing.T) {
	t.Run("falls back to ASS_MCP_PUBLIC_URL when override unset", func(t *testing.T) {
		t.Setenv("ASS_MCP_PUBLIC_URL", "https://mcp.example.com/")

		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig() error = %v", err)
		}
		if cfg.MCPPublicURL != "https://mcp.example.com/" {
			t.Errorf("MCPPublicURL = %q, want %q", cfg.MCPPublicURL, "https://mcp.example.com/")
		}
	})

	t.Run("override wins over ASS_MCP_PUBLIC_URL", func(t *testing.T) {
		t.Setenv("ASS_MCP_PUBLIC_URL", "http://audience-score-system-mcp.ns.svc.cluster.local:8081/")
		t.Setenv("ASS_WEB_MCP_RESOURCE_URL", "http://localhost:8081/")

		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig() error = %v", err)
		}
		if cfg.MCPPublicURL != "http://localhost:8081/" {
			t.Errorf("MCPPublicURL = %q, want %q", cfg.MCPPublicURL, "http://localhost:8081/")
		}
	})

	t.Run("both unset leaves MCPPublicURL empty", func(t *testing.T) {
		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("loadConfig() error = %v", err)
		}
		if cfg.MCPPublicURL != "" {
			t.Errorf("MCPPublicURL = %q, want empty (fail-fast happens downstream in run())", cfg.MCPPublicURL)
		}
	})
}
