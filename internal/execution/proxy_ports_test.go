package execution

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestProxyPortsUsesValidatedExecutionContract(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", "..", "packaging", "go", "execution-contract.json"),
		filepath.Join("..", "..", "packaging", "container", "config", "execution-contract.json"),
	} {
		raw, err := os.ReadFile(path)
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
			t.Fatalf("%s proxy ports = %v", path, ports)
		}
	}
}
