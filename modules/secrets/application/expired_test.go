//go:build linux

package application

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/fault"
	provider "loki/modules/secrets/devtools"
)

func TestRealExpiredSlotCannotBeRegenerated(t *testing.T) {
	binary, fixture := os.Getenv("LOKI_SECRET_TEST_DEVTOOLS"), os.Getenv("LOKI_SECRET_TEST_EXPIRED_FIXTURE")
	if binary == "" || fixture == "" {
		if os.Getenv("LOKI_REQUIRE_SECRET_PROVIDER") == "1" {
			t.Fatal("pinned provider and provider-owned expiry fixture are required")
		}
		t.Skip("native values suite supplies the provider-owned expiry fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	if os.Chmod(root, 0700) != nil {
		t.Fatal("private root")
	}
	data := filepath.Join(root, "provider")
	inbox, err := os.MkdirTemp("/dev/shm", "loki-expired-stage-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(inbox)
	r := controlpolicy.ApplicationResource{ID: "d3cf69e1-02d4-4914-81ad-cd603457cd79", Alias: "expiry", Repository: "github.com/fixture/expiry", Principals: []controlpolicy.ApplicationGrant{{Principal: "agent", Metadata: true, Write: true}}, Environments: []string{"common"}, Keys: map[string]string{"EXPIRED": "secret"}, Network: "none"}
	seed := exec.CommandContext(ctx, fixture, data, r.Profile())
	seed.Stdin = strings.NewReader("synthetic-expiry-passphrase")
	seed.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent"}
	if err := seed.Run(); err != nil {
		t.Fatal("provider fixture did not create the expired slot", err)
	}
	unlock := exec.CommandContext(ctx, binary, "vault", "unlock", "--passphrase-stdin", "--isolated-root", data)
	unlock.Stdin = strings.NewReader("synthetic-expiry-passphrase")
	unlock.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent"}
	if err := unlock.Run(); err != nil {
		t.Fatal("synthetic unlock failed", err)
	}
	t.Cleanup(func() { _ = exec.Command(binary, "vault", "lock", "--isolated-root", data).Run() })
	p, err := provider.New(ctx, binary, data)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Provider: p, Root: root, Inbox: inbox, DeploymentID: "1bbf75ed-1b51-46bd-8255-31536e945564", PolicySHA256: strings.Repeat("e", 64), Resources: []controlpolicy.ApplicationResource{r}})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := p.Revision(ctx, r.Profile(), "common")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Handle(ctx, "agent", "write", appvalues.Request{Action: "generate", Resource: ptr("expiry"), Name: ptr("EXPIRED"), ExpectedRevision: &revision, RequestID: ptr("32c43b27-bc19-438b-b06c-708aab967efe")})
	if err == nil || fault.Describe(err).Code != fault.CodeValueExists {
		t.Fatalf("expired slot was replaced: %v", err)
	}
	after, err := p.Revision(ctx, r.Profile(), "common")
	if err != nil || after != revision {
		t.Fatal("rejected expired-slot creation mutated provider state")
	}
}
