package tools

import (
	"strings"
	"testing"
)

func TestConfigHostAndSelection(t *testing.T) {
	base := Config{Schema: 1, Release: "0.2.0", Host: Host{Kind: "local"}, Mode: ProjectHost, Tools: []Selection{{ID: "browser"}}}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, host := range []Host{{Kind: "local", Address: "host"}, {Kind: "ssh", Address: "-oProxyCommand=bad"}, {Kind: "wsl"}, {Kind: "ssh", Address: "host\nother"}} {
		if err := host.Validate(); err == nil {
			t.Fatalf("accepted invalid host: %#v", host)
		}
	}
	for _, host := range []Host{{Kind: "ssh", Address: "user@host"}, {Kind: "wsl", Distribution: "Ubuntu"}} {
		if err := host.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	base.Tools = append(base.Tools, base.Tools[0])
	if err := base.Validate(); err == nil {
		t.Fatal("accepted duplicate selection")
	}
	if _, err := ParseConfig([]byte(`{"schema":1,"release":"0.2.0","host":{"kind":"local"},"mode":"project-host","tools":[],"unknown":true}`)); err == nil {
		t.Fatal("accepted unknown field")
	}
}

func TestArtifactIdentityAndTrustInputs(t *testing.T) {
	a := Artifact{Module: "browser", Release: "0.2.0", Target: Target{OS: "linux", Arch: "amd64", Mode: ProjectHost}, URL: "https://example.test/browser.tar.gz", SHA256: strings.Repeat("a", 64), Bytes: 100, Format: "tar.gz"}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	b := a
	b.URL = "https://mirror.test/browser.tar.gz"
	if a.Identity() != b.Identity() {
		t.Fatal("mirror changed artifact identity")
	}
	for _, url := range []string{"http://example.test/a", "file:///tmp/a", "https://user:password@example.test/a", "https://example.test/a#fragment"} {
		b = a
		b.URL = url
		if err := b.Validate(); err == nil {
			t.Fatalf("accepted acquisition URL %q", url)
		}
	}
	b = a
	b.SHA256 = strings.Repeat("A", 64)
	if err := b.Validate(); err == nil {
		t.Fatal("accepted noncanonical digest")
	}
}

func TestOperationRecoveryTransitions(t *testing.T) {
	o := Operation{Schema: 1, ID: "op-install", Module: "browser", Action: "install", Phase: Prepared, Candidate: strings.Repeat("a", 64)}
	if _, err := o.Advance(Committed); err == nil {
		t.Fatal("committed without staging")
	}
	staged, err := o.Advance(Staged)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := staged.Advance(Committed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := committed.Advance(Aborted); err == nil {
		t.Fatal("rolled back terminal record")
	}
	if _, err := committed.Advance(Committed); err != nil {
		t.Fatal("idempotent replay:", err)
	}
	if _, err := staged.Advance(Aborted); err != nil {
		t.Fatal("staging recovery:", err)
	}
}
