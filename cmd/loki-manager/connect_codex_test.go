package main

import (
	"bytes"
	"testing"
)

func TestCodexConnectionPreservesUserConfiguration(t *testing.T) {
	old := []byte("# User comment\nmodel = \"example\"\n[mcp_servers.other]\ncommand = \"other\"\n")
	block := []byte(codexBegin + "[mcp_servers.loki_browser]\ncommand = \"/owned/loki\"\n" + codexEnd)
	first, err := mergeCodex(old, block, "loki_browser", codexBegin, codexEnd)
	if err != nil || !bytes.HasPrefix(first, old) {
		t.Fatalf("existing configuration was changed: %s, %v", first, err)
	}
	second, err := mergeCodex(first, block, "loki_browser", codexBegin, codexEnd)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("repeat setup changed configuration: %v", err)
	}
	updated := bytes.ReplaceAll(block, []byte("/owned/loki"), []byte("/new/loki"))
	third, err := mergeCodex(second, updated, "loki_browser", codexBegin, codexEnd)
	if err != nil || !bytes.HasPrefix(third, old) || bytes.Count(third, []byte("[mcp_servers.loki_browser]")) != 1 {
		t.Fatalf("owned update changed unrelated configuration: %v", err)
	}
	for _, invalid := range [][]byte{
		[]byte("[mcp_servers.loki_browser]\ncommand = \"user\"\n"),
		[]byte(codexBegin + "[mcp_servers.loki_browser]\n"),
		[]byte("broken = ["),
	} {
		if _, err := mergeCodex(invalid, block, "loki_browser", codexBegin, codexEnd); err == nil {
			t.Fatalf("unsafe merge accepted: %s", invalid)
		}
	}
}
