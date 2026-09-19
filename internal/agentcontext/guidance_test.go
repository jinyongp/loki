package agentcontext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/policy"
)

func writeGuidance(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveGuidanceUsesActualTargetChain(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "packages", "api")
	sibling := filepath.Join(root, "packages", "web")
	if err := os.MkdirAll(filepath.Join(nested, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sibling, 0755); err != nil {
		t.Fatal(err)
	}
	writeGuidance(t, root, "root rules\n")
	writeGuidance(t, nested, "api rules\n")
	writeGuidance(t, sibling, "web rules\n")

	paths, err := policy.New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer paths.Close()

	result, err := resolveGuidance(paths, "packages/api", "src/new.go")
	if err != nil {
		t.Fatal(err)
	}
	if result.Target != "packages/api/src/new.go" || result.TargetDir != "packages/api/src" || !result.Complete {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Sources) != 2 ||
		result.Sources[0].Path != "AGENTS.md" ||
		result.Sources[1].Path != "packages/api/AGENTS.md" {
		t.Fatalf("sources = %#v", result.Sources)
	}
	if strings.Contains(result.Sources[1].Content, "web") {
		t.Fatal("sibling guidance leaked into target chain")
	}
}

func TestResolveGuidanceRefreshesAndHandlesIncompleteSources(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "pkg")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	writeGuidance(t, root, "v1\n")
	paths, err := policy.New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer paths.Close()

	first, err := resolveGuidance(paths, ".", "pkg/new.go")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("v2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	second, err := resolveGuidance(paths, ".", "pkg/new.go")
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision == second.Revision || first.Sources[0].Revision == second.Sources[0].Revision {
		t.Fatal("guidance revision did not refresh")
	}

	if err := os.WriteFile(filepath.Join(nested, "AGENTS.md"), []byte{0xff, 0xfe}, 0644); err != nil {
		t.Fatal(err)
	}
	incomplete, err := resolveGuidance(paths, ".", "pkg/new.go")
	if err != nil {
		t.Fatal(err)
	}
	if incomplete.Complete || len(incomplete.Diagnostics) != 1 || incomplete.Diagnostics[0].Code != "invalid_utf8" {
		t.Fatalf("incomplete = %#v", incomplete)
	}
}

func TestResolveGuidanceRejectsTraversalAndSymlinks(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "pkg")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	paths, err := policy.New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer paths.Close()

	if _, err := resolveGuidance(paths, ".", "../outside.go"); err == nil {
		t.Fatal("parent traversal accepted")
	}
	outside := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(outside, []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(nested, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveGuidance(paths, ".", "pkg/new.go"); err == nil {
		t.Fatal("AGENTS.md symlink accepted")
	}
}

func TestResolveGuidanceHandlesExistingTargetsAndRemoval(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "pkg")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	writeGuidance(t, root, "root rules\n")
	writeGuidance(t, nested, "pkg rules\n")
	if err := os.WriteFile(filepath.Join(nested, "existing.go"), []byte("package pkg\n"), 0644); err != nil {
		t.Fatal(err)
	}

	paths, err := policy.New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer paths.Close()

	for _, test := range []struct {
		target    string
		targetDir string
	}{
		{target: "pkg/existing.go", targetDir: "pkg"},
		{target: "pkg", targetDir: "pkg"},
	} {
		result, err := resolveGuidance(paths, ".", test.target)
		if err != nil {
			t.Fatalf("resolve %q: %v", test.target, err)
		}
		if result.TargetDir != test.targetDir || len(result.Sources) != 2 || !result.Complete {
			t.Fatalf("resolve %q = %#v", test.target, result)
		}
	}

	before, err := resolveGuidance(paths, ".", "pkg/existing.go")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(nested, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	after, err := resolveGuidance(paths, ".", "pkg/existing.go")
	if err != nil {
		t.Fatal(err)
	}
	if before.Revision == after.Revision || len(after.Sources) != 1 || after.Sources[0].Path != "AGENTS.md" {
		t.Fatalf("removed guidance was not refreshed: before=%#v after=%#v", before, after)
	}
}

func TestResolveGuidanceReportsConfiguredBounds(t *testing.T) {
	t.Run("source size", func(t *testing.T) {
		root := t.TempDir()
		writeGuidance(t, root, strings.Repeat("x", maxGuidanceFileBytes+1))
		paths, err := policy.New(root)
		if err != nil {
			t.Fatal(err)
		}
		defer paths.Close()

		result, err := resolveGuidance(paths, ".", ".")
		if err != nil {
			t.Fatal(err)
		}
		if result.Complete || len(result.Sources) != 0 || len(result.Diagnostics) != 1 ||
			result.Diagnostics[0].Code != "oversized" {
			t.Fatalf("oversized result = %#v", result)
		}
	})

	t.Run("source count", func(t *testing.T) {
		root := t.TempDir()
		writeGuidance(t, root, "root\n")
		current := root
		for index := 0; index < maxGuidanceSources; index++ {
			current = filepath.Join(current, "d")
			writeGuidance(t, current, "nested\n")
		}
		target, err := filepath.Rel(root, current)
		if err != nil {
			t.Fatal(err)
		}
		paths, err := policy.New(root)
		if err != nil {
			t.Fatal(err)
		}
		defer paths.Close()

		result, err := resolveGuidance(paths, ".", filepath.ToSlash(target))
		if err != nil {
			t.Fatal(err)
		}
		if result.Complete || len(result.Sources) != maxGuidanceSources || len(result.Diagnostics) != 1 ||
			result.Diagnostics[0].Code != "too_many_sources" {
			t.Fatalf("source-count result = %#v", result)
		}
	})

	t.Run("target depth", func(t *testing.T) {
		root := t.TempDir()
		current := root
		for index := 0; index < maxGuidanceDepth; index++ {
			current = filepath.Join(current, "d")
			if err := os.Mkdir(current, 0755); err != nil {
				t.Fatal(err)
			}
		}
		target, err := filepath.Rel(root, current)
		if err != nil {
			t.Fatal(err)
		}
		paths, err := policy.New(root)
		if err != nil {
			t.Fatal(err)
		}
		defer paths.Close()

		if _, err := resolveGuidance(paths, ".", filepath.ToSlash(target)); err == nil ||
			!strings.Contains(err.Error(), "depth exceeds") {
			t.Fatalf("depth error = %v", err)
		}
	})
}
