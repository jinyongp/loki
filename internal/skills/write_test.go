package skills

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSkillMutationAndGuards(t *testing.T) {
	r := fixture(t)
	created, err := r.Create("project", "repo", "new-skill", " A skill ", "Original.\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if created["source"] != "repo/.agents/skills/new-skill" {
		t.Fatal(created)
	}
	if _, err := r.Create("project", "repo", "new-skill", "Replacement", "Overwrite", nil); err == nil {
		t.Fatal("duplicate creation")
	}
	if _, err := r.Create("project", ".", "bad-skill", "A", "B", nil); err == nil {
		t.Fatal("project outside repository")
	}
	if _, err := r.Create("shared", ".", "bad-skill", "A", "B", map[string]any{"name": "other"}); err == nil {
		t.Fatal("metadata override")
	}
	patch := "--- a/SKILL.md\n+++ b/SKILL.md\n@@ -4,4 +4,4 @@\n description: A skill\n ---\n \n-Original.\n+Updated.\n"
	// The hunk starts at the description line in the generated document.
	patch = strings.Replace(patch, "@@ -4,4 +4,4 @@", "@@ -3,4 +3,4 @@", 1)
	updated, err := r.Edit(t.Context(), "new-skill", "repo", patch, created["sha256"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if updated["sha256"] == created["sha256"] {
		t.Fatal("unchanged digest")
	}
	if _, err := r.Edit(t.Context(), "new-skill", "repo", patch, created["sha256"].(string)); err == nil {
		t.Fatal("stale patch")
	}
	for _, bad := range []string{strings.ReplaceAll(patch, "SKILL.md", "other.txt"), patch + "diff --git a/other b/other\nnew file mode 100644\n", strings.Replace(patch, "+Updated.", "+", 1)} {
		if _, err := r.Edit(t.Context(), "new-skill", "repo", bad, updated["sha256"].(string)); err == nil {
			t.Fatal("invalid patch accepted")
		}
	}
	resource, err := r.WriteResource("new-skill", "references/guide.txt", "first", "repo", false, nil, "utf-8")
	if err != nil {
		t.Fatal(err)
	}
	old := resource["sha256"].(string)
	if _, err := r.WriteResource("new-skill", "references/guide.txt", "second", "repo", false, nil, "utf-8"); err == nil {
		t.Fatal("overwrite without flag")
	}
	if _, err := r.WriteResource("new-skill", "references/new.txt", "second", "repo", true, &old, "utf-8"); err == nil {
		t.Fatal("hash for new file")
	}
	if _, err := r.WriteResource("new-skill", "references/guide.txt", "second", "repo", true, &old, "utf-8"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.WriteResource("new-skill", "references/guide.txt", "third", "repo", true, &old, "utf-8"); err == nil {
		t.Fatal("stale resource hash")
	}
	if _, err := r.WriteResource("new-skill", "assets/a.bin", "/wAB", "repo", false, nil, "base64"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"%%%", "/wAB\n"} {
		if _, err := r.WriteResource("new-skill", "assets/b.bin", bad, "repo", false, nil, "base64"); err == nil {
			t.Fatal("invalid base64")
		}
	}
	outside := t.TempDir()
	link := filepath.Join(r.Workspace.Root(), "repo/.agents/skills/new-skill/scripts")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := r.WriteResource("new-skill", "scripts/leak", "bad", "repo", false, nil, "utf-8"); err == nil {
		t.Fatal("symlink write")
	}
	if _, err := os.Stat(filepath.Join(outside, "leak")); !os.IsNotExist(err) {
		t.Fatal("outside file created")
	}
	if err := os.MkdirAll(filepath.Join(r.Builtin.Root(), "builtin-only"), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := document("builtin-only", "Built in", "Read only.", nil)
	if err := os.WriteFile(filepath.Join(r.Builtin.Root(), "builtin-only/SKILL.md"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.WriteResource("builtin-only", "assets/new", "bad", "repo", false, nil, "utf-8"); err == nil {
		t.Fatal("builtin write")
	}
	if _, err := r.Edit(t.Context(), "builtin-only", "repo", patch, digest(data)); err == nil {
		t.Fatal("builtin edit")
	}
}

func TestSkillConcurrentResourceCAS(t *testing.T) {
	r := fixture(t)
	initial, err := r.WriteResource("example", "references/cas.txt", "initial", "repo", false, nil, "utf-8")
	if err != nil {
		t.Fatal(err)
	}
	expected := initial["sha256"].(string)
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, text := range []string{"first", "second"} {
		wg.Go(func() {
			_, err := r.WriteResource("example", "references/cas.txt", text, "repo", true, &expected, "utf-8")
			results <- err
		})
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("CAS successes=%d", success)
	}
}
