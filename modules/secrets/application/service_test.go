//go:build linux

package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"loki/internal/contract"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/fault"
	provider "loki/modules/secrets/devtools"
)

func ptr[T any](v T) *T { return &v }

func TestRealApplicationValues(t *testing.T) {
	binary := os.Getenv("LOKI_SECRET_TEST_DEVTOOLS")
	if binary == "" {
		if os.Getenv("LOKI_REQUIRE_SECRET_PROVIDER") == "1" {
			t.Fatal("pinned provider required")
		}
		t.Skip("pinned provider is exercised by the native values Actions suite")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	inbox, err := os.MkdirTemp("/dev/shm", "loki-values-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(inbox)
	for _, directory := range []struct {
		path  string
		tmpfs bool
	}{{root, false}, {inbox, true}} {
		if err := privateDirectory(directory.path, directory.tmpfs); err != nil {
			info, _ := os.Stat(directory.path)
			t.Fatalf("fixture directory %s mode=%v: %v", directory.path, info.Mode(), err)
		}
	}
	dataRoot := filepath.Join(root, "provider")
	p, err := provider.New(ctx, binary, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	r := controlpolicy.ApplicationResource{ID: "c199846b-43ad-468c-9229-11976d4e5b3a", Alias: "demo", Repository: "github.com/fixture/app", Principals: []controlpolicy.ApplicationGrant{{Principal: "agent", Metadata: true, Write: true, Execute: true}}, Environments: []string{"common", "test"}, Keys: map[string]string{"TOKEN": "secret", "STAGED": "secret", "GONE": "secret", "KIND": "secret", "PUB": "variable"}, Network: "none"}
	options := Options{Provider: p, Root: root, Inbox: inbox, DeploymentID: "1bbf75ed-1b51-46bd-8255-31536e945564", PolicySHA256: strings.Repeat("a", 64), Resources: []controlpolicy.ApplicationResource{r}}
	s, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	sequence := 0
	id := func() string { sequence++; return fmt.Sprintf("9bc5f490-949a-445f-ac76-%012d", sequence) }
	run := func(input string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, append(args, "--isolated-root", dataRoot)...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent"}
		cmd.Stdin = strings.NewReader(input)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("synthetic provider command %s failed: %v", args[0], err)
		}
	}
	status, err := s.Handle(ctx, "agent", "inspect", appvalues.Request{Action: "status", Resource: ptr("demo")})
	if err != nil || status["state"] != "uninitialized" {
		t.Fatalf("cold status: %v %v", status, err)
	}
	run("synthetic-private-passphrase", "vault", "init", "--backend", "passphrase", "--passphrase-stdin")
	t.Cleanup(func() {
		command := exec.Command(binary, "vault", "lock", "--isolated-root", dataRoot)
		_ = command.Run()
	})
	revision := func(env string) string {
		t.Helper()
		v, e := p.Revision(ctx, r.Profile(), env)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	write := func(action, name string) appvalues.Request {
		return appvalues.Request{Action: action, Resource: ptr("demo"), Name: ptr(name), ExpectedRevision: ptr(revision("common")), RequestID: ptr(id())}
	}
	call := func(op string, q appvalues.Request) map[string]any {
		t.Helper()
		out, e := s.Handle(ctx, "agent", op, q)
		if e != nil {
			t.Fatalf("%s: %v", q.Action, e)
		}
		raw, _ := json.Marshal(contract.SecretV2OutputSchema("secret_" + op))
		var schema jsonschema.Schema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ = json.Marshal(out)
		var wire map[string]any
		_ = json.Unmarshal(raw, &wire)
		if err := resolved.Validate(wire); err != nil {
			t.Fatalf("actual %s output violated the public contract: %v", q.Action, err)
		}
		return out
	}
	code := func(op string, q appvalues.Request, want fault.Code) {
		t.Helper()
		_, e := s.Handle(ctx, "agent", op, q)
		if e == nil || fault.Describe(e).Code != want {
			t.Fatalf("%s: wanted %s, got %v", q.Action, want, e)
		}
	}

	empty := write("set_public", "PUB")
	empty.Value = ptr("")
	first := call("write", empty)
	if first["changed"] != true {
		t.Fatal("empty public value was not created")
	}
	changed := empty
	changed.Value = ptr("different")
	code("write", changed, fault.CodeConflict)
	stale := write("set_public", "PUB")
	stale.ExpectedRevision = empty.ExpectedRevision
	stale.Value = ptr("stale")
	code("write", stale, fault.CodeConflict)
	// Two actual provider writers race at the same revision: exactly one wins.
	q1 := write("set_public", "PUB")
	q1.Value = ptr("one")
	q2 := q1
	q2.RequestID = ptr(id())
	q2.Value = ptr("two")
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, q := range []appvalues.Request{q1, q2} {
		wg.Go(func() { _, e := s.Handle(ctx, "agent", "write", q); results <- e })
	}
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for e := range results {
		if e == nil {
			wins++
		} else if fault.Describe(e).Code == fault.CodeConflict {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("concurrent revision results: %d %d", wins, conflicts)
	}
	generate := write("generate", "TOKEN")
	_ = call("write", generate) // Simulate an entirely lost public response.
	later := write("set_public", "PUB")
	later.Value = ptr("later")
	call("write", later)
	// A new service instance has no in-memory result or private input cache.
	s, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	replayed := call("write", generate)
	if replayed["replayed"] != true || replayed["changed"] != true || replayed["bytes"] != 32 {
		t.Fatalf("original generation result was not preserved: %v", replayed)
	}
	code("write", write("generate", "TOKEN"), fault.CodeValueExists)
	code("write", write("generate", "PUB"), fault.CodeNotAuthorized)
	// An inconsistent existing provider kind is a conflict, never an implicit
	// conversion into the kind declared by the host resource ceiling.
	if _, err := p.SetPublic(ctx, r.Profile(), "common", "KIND", "kind-fixture-public", revision("common"), id()); err != nil {
		t.Fatal(err)
	}
	code("write", write("generate", "KIND"), fault.CodeConflict)
	if _, err := p.Unset(ctx, r.Profile(), "common", "variable", "KIND", revision("common"), id()); err != nil {
		t.Fatal(err)
	}
	code("write", write("generate", "LOKI_TOKEN"), fault.CodeNotAuthorized)
	if _, e := s.Handle(ctx, "other-agent", "inspect", appvalues.Request{Action: "values", Resource: ptr("demo")}); fault.Describe(e).Code != fault.CodeNotAuthorized {
		t.Fatal("foreign principal admitted")
	}

	// Exact environment layer creation may override a common secret. Removing
	// that layer returns its original common fallback, even after later edits.
	run("", "env", "create", "test", "--profile", r.Profile(), "--revision", revision("common"), "--request-id", id())
	emptyAdmin := AdminRequest{Action: "set_secret", Resource: "demo", Name: "GONE", Revision: revision("common"), RequestID: id(), Empty: true}
	if _, err := s.Admin(ctx, emptyAdmin, nil); err != nil {
		t.Fatal(err)
	}
	code("write", write("generate", "GONE"), fault.CodeValueExists)
	emptyAdmin.Revision = revision("common")
	emptyAdmin.RequestID = id()
	if _, err := s.Admin(ctx, emptyAdmin, nil); fault.Describe(err).Code != fault.CodeValueExists {
		t.Fatalf("administrator overwrite was implicit: %v", err)
	}
	emptyAdmin.Replace = true
	emptyAdmin.RequestID = id()
	if _, err := s.Admin(ctx, emptyAdmin, nil); err != nil {
		t.Fatal(err)
	}
	removeEmpty := appvalues.Request{Action: "unset", Resource: ptr("demo"), Name: ptr("GONE"), Kind: ptr("secret"), ExpectedRevision: ptr(revision("common")), RequestID: ptr(id())}
	call("delete", removeEmpty)
	envGenerate := write("generate", "TOKEN")
	envGenerate.Environment = ptr("test")
	envGenerate.ExpectedRevision = ptr(revision("test"))
	call("write", envGenerate)
	unset := appvalues.Request{Action: "unset", Resource: ptr("demo"), Environment: ptr("test"), Name: ptr("TOKEN"), Kind: ptr("secret"), ExpectedRevision: ptr(revision("test")), RequestID: ptr(id())}
	fallback := call("delete", unset)
	if fallback["effective_present"] != true || fallback["source"] != "common" {
		t.Fatalf("fallback: %v", fallback)
	}
	later = write("set_public", "PUB")
	later.Value = ptr("after-unset")
	call("write", later)
	if repeated := call("delete", unset); repeated["replayed"] != true || repeated["source"] != "common" {
		t.Fatal("unset replay changed")
	}

	stage, err := s.Stage(ctx, StageRequest{Resource: "demo", Principal: "agent"}, []byte("STAGED=synthetic-staged-canary\n"))
	if err != nil {
		t.Fatal(err)
	}
	importQ := appvalues.Request{Action: "import_staged", Resource: ptr("demo"), StageID: ptr(stage["stage_id"].(string)), ExpectedRevision: ptr(revision("common")), RequestID: ptr(id())}
	call("write", importQ)
	if _, e := os.Stat(filepath.Join(inbox, *importQ.StageID+".data")); !os.IsNotExist(e) {
		t.Fatal("staged plaintext survived success")
	}
	s, err = New(options)
	if err != nil {
		t.Fatal(err)
	}
	if result := call("write", importQ); result["replayed"] != true || result["count"] != 1 {
		t.Fatal("receipt-first stage replay failed")
	}
	missing, err := s.Stage(ctx, StageRequest{Resource: "demo", Principal: "agent"}, []byte("GONE=synthetic-disappearing-canary\n"))
	if err != nil {
		t.Fatal(err)
	}
	missingQ := appvalues.Request{Action: "import_staged", Resource: ptr("demo"), StageID: ptr(missing["stage_id"].(string)), ExpectedRevision: ptr(revision("common")), RequestID: ptr(id())}
	if err := s.discardStage(*missingQ.StageID); err != nil {
		t.Fatal(err)
	}
	code("write", missingQ, fault.CodeStageExpired)

	values := call("inspect", appvalues.Request{Action: "values", Resource: ptr("demo"), Limit: ptr(1)})
	cursor, ok := values["next_cursor"].(string)
	if !ok {
		t.Fatal("bounded page omitted continuation")
	}
	later = write("set_public", "PUB")
	later.Value = ptr("invalidate-page")
	call("write", later)
	code("inspect", appvalues.Request{Action: "values", Resource: ptr("demo"), Cursor: &cursor}, fault.CodeConflict)
	audit := call("inspect", appvalues.Request{Action: "audit", Resource: ptr("demo")})
	encoded, _ := json.Marshal(audit)
	for _, forbidden := range []string{"synthetic-staged-canary", "synthetic-disappearing-canary", "after-unset", "fingerprint", ".data", "passphrase"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("private input appeared in public audit: %s", forbidden)
		}
	}
	run("", "vault", "lock")
	status = call("inspect", appvalues.Request{Action: "status", Resource: ptr("demo")})
	if status["state"] != "locked" || status["integrity_verified"] != false {
		t.Fatalf("locked protection status: %v", status)
	}
	locked := write("generate", "GONE")
	code("write", locked, fault.CodeVaultLocked)
}
