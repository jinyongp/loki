package buildinfo

import "testing"

func TestString(t *testing.T) {
	oldVersion, oldCommit, oldDate := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = oldVersion, oldCommit, oldDate })
	Version, Commit, Date = "1.2.3", "abc123", "2026-09-04"
	if got := String(); got != "loki 1.2.3 abc123 2026-09-04" {
		t.Fatalf("String() = %q", got)
	}
}
