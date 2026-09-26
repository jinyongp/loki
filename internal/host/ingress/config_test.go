package ingress

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeCanonicalizesAndBoundsHosts(t *testing.T) {
	got, err := Normalize([]string{"B.example.com", "a.example.com", "b.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"a.example.com", "b.example.com"}) {
		t.Fatalf("canonical hosts = %#v", got)
	}
	if _, err = Normalize([]string{"https://bad.example"}); err == nil || !strings.Contains(err.Error(), "ingress hostname is invalid") {
		t.Fatalf("invalid host error = %v", err)
	}
	hosts := make([]string, 0, 65)
	for i := 0; i < 65; i++ {
		hosts = append(hosts, "h"+strings.Repeat("a", i/26)+string(rune('a'+i%26))+".example.com")
	}
	if _, err = Normalize(hosts); err == nil || !strings.Contains(err.Error(), "exceeds 64") {
		t.Fatalf("oversized allowlist error = %v", err)
	}
}

func TestRenderProducesStrictIngressFragment(t *testing.T) {
	raw, err := Render([]string{"MCP.Example.com"})
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "public_hosts = ['mcp.example.com']") &&
		!strings.Contains(text, "public_hosts = [\"mcp.example.com\"]") {
		t.Fatalf("ingress TOML = %q", text)
	}
	if strings.Contains(text, "port") || strings.Contains(text, "host =") {
		t.Fatalf("ingress projection widened configuration authority: %q", text)
	}
}
