//go:build linux

package management

import "testing"

func TestCandidateProtectionMetadataAdmitsOnlyReviewedDiagnostics(t *testing.T) {
	for _, test := range []struct{ action, raw string }{
		{"status", `{"action":"status","state":"uninitialized"}`},
		{"status", `{"action":"status","state":"unlocked","backend":"passphrase","idle_expires_at":"2026-10-23T13:00:00.000000001Z"}`},
		{"unlock", `{"action":"unlock","state":"unlocked","changed":true,"backend":"passphrase","idle_expires_at":"2026-10-23T13:00:00Z"}`},
	} {
		if err := validateCandidateProtectionMetadata([]byte(test.raw), test.action); err != nil {
			t.Fatal("reviewed candidate metadata rejected", err)
		}
	}
	for _, raw := range []string{
		`{"action":"status","state":"unlocked","secret":"private"}`,
		`{"action":"status","state":"unlocked","backend":"native"}`,
		`{"action":"status","state":"unlocked","idle_expires_at":"invalid"}`,
		`{"action":"status","state":"unlocked","backend":null}`,
		`{"action":"unlock","state":"unlocked"}`,
		`{"action":"status","state":"unlocked","rewrap":{"request_id":"d62d2ff3-0e6c-4698-9ca2-a5fca2d8aa0e","from_backend":"passphrase","backend":"passphrase","slot_published":true,"completed":false,"integrity_verified":false}}`,
	} {
		if err := validateCandidateProtectionMetadata([]byte(raw), "status"); err == nil {
			t.Fatal("unknown or unresolved candidate metadata admitted")
		}
	}
}
