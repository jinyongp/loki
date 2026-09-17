package main

import (
	"testing"

	"loki/internal/config"
)

func TestGoServicePortDefaults(t *testing.T) {
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 18765 {
		t.Fatalf("MCP default = %d", c.Port)
	}
	if defaultEgressProxyPort != 18766 {
		t.Fatalf("egress default = %d", defaultEgressProxyPort)
	}
	if defaultBrowserProxyPort != 18767 || defaultBrowserProxyURL != "http://127.0.0.1:18767" {
		t.Fatalf("browser defaults = %d %q", defaultBrowserProxyPort, defaultBrowserProxyURL)
	}
}
