package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"loki/internal/execution"
	"loki/internal/portguard"
)

func TestProtectedPortPolicyUsesMCPAndExecutionContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "packaging", "native", "execution-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	contract, err := execution.Load(raw)
	if err != nil {
		t.Fatal(err)
	}
	ports, err := ProtectedPortPolicy(18765, contract)
	if err != nil {
		t.Fatal(err)
	}
	for _, port := range []int{18765, 18766, 18767} {
		if !errors.Is(ports.Validate(port), portguard.ErrProtected) {
			t.Fatalf("port %d is not protected", port)
		}
	}
}
