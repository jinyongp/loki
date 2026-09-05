package secret

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
	"loki/internal/project"
)

func fixture(t *testing.T) Controller {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	repo := filepath.Join(workspace, "project")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("/usr/bin/git", args...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %s %v", out, err)
		}
	}
	for _, name := range []string{"project", "other"} {
		path := filepath.Join(workspace, name)
		git("init", "-q", path)
		git("-C", path, "-c", "commit.gpgsign=false", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "fixture")
	}
	git("-C", repo, "worktree", "add", "-q", "-b", "feature", filepath.Join(workspace, "feature"))
	projects, err := project.New(workspace, filepath.Join(root, "projects"))
	if err != nil {
		t.Fatal(err)
	}
	c := Controller{StateDirectory: filepath.Join(root, "runtime"), Projects: projects}
	if _, err = c.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	return c
}
func encoded(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func action(cwd string) map[string]any {
	return map[string]any{"command": []string{"pnpm", "dev", "--port", "{LOKI_PORT}"}, "cwd": cwd, "secrets": []string{}, "all_secrets": true, "required_secrets": []string{"TOKEN"}, "timeout_seconds": 3600, "max_output_bytes": 1048576, "dynamic_port": map[string]any{"preferred": 42100, "environment": "PORT", "origin_environment": "PUBLIC_ORIGIN"}, "public_environment": []string{"PUBLIC_API"}, "preview_environment": map[string]string{"PUBLIC_API": "/api"}, "singleton": true, "lock_probe": ".tmp/owner.lock"}
}
func TestSecretMetadataAndAtomicReferences(t *testing.T) {
	c := fixture(t)
	ctx := t.Context()
	check := func(out map[string]any, err error) map[string]any {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		data := encoded(t, out)
		if bytes.Contains(data, []byte("synthetic-private")) {
			t.Fatal("secret value leaked into metadata")
		}
		return out
	}
	check(c.CreateProfile(ctx, "web"))
	check(c.ImportValues(ctx, "web", map[string]string{"TOKEN": "", "PRIVATE": "synthetic-private", "PUBLIC_API": "http://127.0.0.1:41280/v1"}))
	check(c.SetAction(ctx, "web", "dev", encoded(t, action("project"))))
	meta := check(c.Profile(ctx, "web"))
	policy := meta["action_policies"].(map[string]any)["dev"].(map[string]any)
	if meta["configured_secret_count"] != 2 || policy["ready"] != false || policy["missing_required_secrets"].([]string)[0] != "TOKEN" {
		t.Fatalf("readiness metadata %v", meta)
	}
	check(c.Generate(ctx, "web", "TOKEN", 32))
	doc, err := c.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	generated := object(object(object(doc["profiles"])["web"])["secrets"])["TOKEN"].(string)
	if decoded, err := base64.RawURLEncoding.DecodeString(generated); err != nil || len(decoded) != 32 {
		t.Fatal("generated entropy size changed")
	}
	meta = check(c.Profile(ctx, "web"))
	if bytes.Contains(encoded(t, meta), []byte(generated)) {
		t.Fatal("generated secret returned")
	}
	if meta["action_policies"].(map[string]any)["dev"].(map[string]any)["ready"] != true {
		t.Fatal("ready did not reflect configured secret")
	}
	if _, err = c.Generate(ctx, "web", "TOKEN", 32); err == nil {
		t.Fatal("configured secret overwritten")
	}
	before, err := os.ReadFile(filepath.Join(c.StateDirectory, "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.RemoveSecret(ctx, "web", "TOKEN"); err == nil || !strings.Contains(err.Error(), "still referenced") {
		t.Fatalf("referenced removal %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(c.StateDirectory, "store.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("failed removal changed encrypted state")
	}
	check(c.Register(ctx, "project", nil))
	w := map[string]any{"steps": [][]string{{"web", "dev"}}, "required_secrets": map[string][]string{"web": {"TOKEN"}}, "timeout_seconds": 3600}
	check(c.SetWorkflow(ctx, "feature", "development", encoded(t, w)))
	registration := check(c.Registration(ctx, "feature"))
	if registration["registered"] != true || registration["repository"] != "project" {
		t.Fatalf("registration %v", registration)
	}
	workflow := check(c.Workflow(ctx, "project", "development"))
	if workflow["cwd"] != "project" {
		t.Fatal("workflow cwd changed")
	}
	for _, remove := range []func() (map[string]any, error){func() (map[string]any, error) { return c.RemoveProfile(ctx, "web") }, func() (map[string]any, error) { return c.RemoveAction(ctx, "web", "dev") }} {
		before, _ := os.ReadFile(filepath.Join(c.StateDirectory, "store.json"))
		if _, err := remove(); err == nil {
			t.Fatal("workflow reference removed")
		}
		after, _ := os.ReadFile(filepath.Join(c.StateDirectory, "store.json"))
		if !bytes.Equal(before, after) {
			t.Fatal("reference failure changed ciphertext")
		}
	}
	check(c.SetAction(ctx, "web", "other", encoded(t, action("other"))))
	w["steps"] = [][]string{{"web", "other"}}
	if _, err = c.SetWorkflow(ctx, "project", "wrong-repository", encoded(t, w)); err == nil || !strings.Contains(err.Error(), "another Git repository") {
		t.Fatalf("foreign workflow %v", err)
	}
	check(c.RemoveWorkflow(ctx, "project", "development"))
	check(c.RemoveAction(ctx, "web", "dev"))
	check(c.RemoveAction(ctx, "web", "other"))
	check(c.RemoveSecret(ctx, "web", "TOKEN"))
	check(c.RemoveProfile(ctx, "web"))
	check(c.Unregister(ctx, "project"))
	if bytes.Contains(after, []byte(generated)) || bytes.Contains(after, []byte("synthetic-private")) {
		t.Fatal("plaintext in encrypted store")
	}
}
func TestPrivateImportsAndFailures(t *testing.T) {
	c := fixture(t)
	ctx := t.Context()
	if _, err := c.CreateProfile(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(c.inbox(), 0700); err != nil {
		t.Fatal(err)
	}
	id := "0123456789abcdef0123456789abcdef"
	path := filepath.Join(c.inbox(), id+".env")
	if err := os.WriteFile(path, []byte("# fixture\nexport TOKEN='synthetic-private'\nPUBLIC=\"line\\n한글\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	listed, err := c.ListImports()
	if err != nil || len(listed["imports"].([]map[string]any)) != 1 {
		t.Fatalf("imports %v %v", listed, err)
	}
	result, err := c.ImportStaged(ctx, "web", id)
	if err != nil || result["count"] != 2 || result["source_deleted"] != true || bytes.Contains(encoded(t, result), []byte("synthetic-private")) {
		t.Fatalf("import metadata %v %v", result, err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("import source not consumed")
	}
	for _, tc := range []struct {
		data []byte
		mode os.FileMode
	}{{[]byte("INVALID"), 0600}, {[]byte{0xff}, 0600}, {[]byte("TOKEN=fixture\n"), 0644}, {[]byte("TOKEN=" + strings.Repeat("x", MaxSecretBytes+1)), 0600}} {
		if err = os.WriteFile(path, tc.data, 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.Chmod(path, tc.mode); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(filepath.Join(c.StateDirectory, "store.json"))
		if _, err = c.ImportStaged(ctx, "web", id); err == nil {
			t.Fatal("unsafe import accepted")
		}
		after, _ := os.ReadFile(filepath.Join(c.StateDirectory, "store.json"))
		if !bytes.Equal(before, after) {
			t.Fatal("failed import changed state")
		}
		if _, err = os.Stat(path); err != nil {
			t.Fatal("failed import consumed source")
		}
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ImportStaged(ctx, "web", id); err == nil {
		t.Fatal("FIFO accepted")
	}
	if _, err = c.ImportStaged(ctx, "web", "../escape"); err == nil {
		t.Fatal("import traversal accepted")
	}
}
func TestConcurrentMutationsAndExtensionPreservation(t *testing.T) {
	c := fixture(t)
	ctx := t.Context()
	if _, err := c.CreateProfile(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	_, err := c.backend().Update(ctx, nil, func(data json.RawMessage) (json.RawMessage, error) {
		doc, err := decode(data)
		if err != nil {
			return nil, err
		}
		doc["future"] = map[string]any{"revision": json.Number("9007199254740993")}
		object(object(doc["profiles"])["web"])["extension"] = []any{"keep"}
		return json.Marshal(doc)
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 24 {
		wg.Go(func() {
			key := string(rune('A' + i))
			if _, err := c.SetSecret(ctx, "web", key, "fixture", false); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	doc, err := c.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if object(doc["future"])["revision"] != json.Number("9007199254740993") || len(object(object(object(doc["profiles"])["web"])["secrets"])) != 24 || object(object(doc["profiles"])["web"])["extension"] == nil {
		t.Fatal("concurrent updates or extensions lost")
	}
}
func TestPublicConfigAndGenerationBounds(t *testing.T) {
	c := fixture(t)
	ctx := context.Background()
	if _, err := c.CreateProfile(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"nul\x00", strings.Repeat("x", 65537)} {
		if _, err := c.SetSecret(ctx, "web", "PUBLIC", value, true); err == nil {
			t.Fatal("invalid public config accepted")
		}
	}
	for _, count := range []int{0, 15, 129} {
		if _, err := c.Generate(ctx, "web", "TOKEN", count); err == nil {
			t.Fatal("invalid secret byte count accepted")
		}
	}
	if _, err := c.SetSecret(ctx, "web", "PUBLIC", "", true); err != nil {
		t.Fatal(err)
	}
}
