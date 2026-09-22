package execution

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestProxyPortsUsesValidatedExecutionContract(t *testing.T) {
	for _, test := range []struct {
		path, egress, browser string
	}{
		{filepath.Join("..", "..", "packaging", "native", "execution-contract.json"), "http://127.0.0.1:18766", "http://127.0.0.1:18767"},
		{filepath.Join("..", "..", "packaging", "images", "config", "execution-contract.json"), "http://egress:18766", "http://browser-proxy:18767"},
	} {
		raw, err := os.ReadFile(test.path)
		if err != nil {
			t.Fatal(err)
		}
		contract, err := Load(raw)
		if err != nil {
			t.Fatal(err)
		}
		ports, err := contract.ProxyPorts()
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(ports, []int{18766, 18767}) {
			t.Fatalf("%s proxy ports = %v", test.path, ports)
		}
		for profile, want := range map[string]string{"dependency-install": test.egress, "browser": test.browser} {
			endpoint, port, err := contract.ProxyEndpoint(profile)
			if err != nil || endpoint != want || port != map[string]int{"dependency-install": 18766, "browser": 18767}[profile] {
				t.Fatalf("%s %s endpoint = %q %d %v", test.path, profile, endpoint, port, err)
			}
		}
	}
}
