package main

import (
	"bytes"
	"os"
	"testing"
)

func TestFullConnectionRequiresServiceRoleAndFixedAuthority(t *testing.T) {
	for _, args := range [][]string{{"--endpoint", "https://unowned.invalid"}, {"--token-file", "/tmp/unowned"}} {
		var diagnostics bytes.Buffer
		if runFullMCPConnection(args, &diagnostics) != 2 {
			t.Fatal("private adapter accepted destination or credential overrides")
		}
	}
	if os.Geteuid() != 10000 {
		var diagnostics bytes.Buffer
		if runFullMCPConnection(nil, &diagnostics) != 2 {
			t.Fatal("private adapter accepted another service identity")
		}
	}
}
