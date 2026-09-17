package portguard

import (
	"errors"
	"testing"
)

func TestPolicyValidatesRangeAndProtectedPorts(t *testing.T) {
	policy, err := NewPolicy(18765, 18766, 18767, 18766)
	if err != nil {
		t.Fatal(err)
	}
	for _, port := range []int{18765, 18766, 18767} {
		if err := policy.Validate(port); !errors.Is(err, ErrProtected) {
			t.Fatalf("port %d error = %v", port, err)
		}
	}
	if err := policy.Validate(43000); err != nil {
		t.Fatalf("ordinary development port rejected: %v", err)
	}
	for _, port := range []int{0, 1023, 65536} {
		if ValidateNumber(port) == nil {
			t.Fatalf("invalid port %d accepted", port)
		}
	}
	if _, err := NewPolicy(0); err == nil {
		t.Fatal("invalid protected port accepted")
	}
}

func TestZeroPolicyHasNoHistoricalProtectedPorts(t *testing.T) {
	var policy Policy
	for _, port := range []int{8765, 8766, 8767, 18765, 18766, 18767} {
		if err := policy.Validate(port); err != nil {
			t.Fatalf("zero policy rejected %d: %v", port, err)
		}
	}
}
