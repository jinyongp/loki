package agentcontext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/policy"
)

func writeSkill(t *testing.T, root, name, description string, resource string) string {
	t.Helper()
	dir := filepath.Join(root, ".agents", "skills", name)
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	if resource != "" {
		if err := os.WriteFile(filepath.Join(dir, "references", "guide.md"), []byte(resource), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func writePackagedSkill(t *testing.T, root, name, description, resource string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	if resource != "" {
		if err := os.WriteFile(filepath.Join(dir, "references", "guide.md"), []byte(resource), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSkillDiscoveryUsesProjectUserPackagedPrecedenceAndOnDemandBodies(t *testing.T) {
	projectRoot := t.TempDir()
	userHome := t.TempDir()
	packagedRoot := t.TempDir()
	writePackagedSkill(t, packagedRoot, "shared-skill", "Packaged version.", "packaged guide")
	writePackagedSkill(t, packagedRoot, "packaged-only", "Packaged only.", "packaged only guide")
	writeSkill(t, userHome, "shared-skill", "User version.", "user guide")
	writeSkill(t, userHome, "user-only", "User only.", "")
	writeSkill(t, projectRoot, "shared-skill", "Project version.", "project guide")

	projectPaths, err := policy.New(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer projectPaths.Close()

	provider := &Provider{UserHome: userHome, PackagedSkills: packagedRoot}
	catalog, err := provider.listSkills(projectPaths)
	if err != nil {
		t.Fatal(err)
	}
	if !catalog.Complete || len(catalog.Items) != 3 ||
		catalog.Items[0].Name != "packaged-only" || catalog.Items[0].Scope != "packaged" ||
		catalog.Items[1].Name != "shared-skill" || catalog.Items[1].Scope != "project" ||
		catalog.Items[2].Name != "user-only" || catalog.Items[2].Scope != "user" {
		t.Fatalf("catalog = %#v", catalog)
	}
	if len(catalog.Shadowed) != 2 ||
		catalog.Shadowed[0].Name != "shared-skill" || catalog.Shadowed[0].SelectedScope != "project" || catalog.Shadowed[0].ShadowedScope != "packaged" ||
		catalog.Shadowed[1].Name != "shared-skill" || catalog.Shadowed[1].SelectedScope != "project" || catalog.Shadowed[1].ShadowedScope != "user" {
		t.Fatalf("shadowed = %#v", catalog.Shadowed)
	}

	detail, err := provider.inspectEffectiveSkill(projectPaths, "shared-skill")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Item.Scope != "project" || !strings.Contains(detail.Item.Content, "# shared-skill") ||
		len(detail.Item.Resources) != 1 || detail.Item.Resources[0].Path != "references/guide.md" {
		t.Fatalf("detail = %#v", detail)
	}
	packaged, err := provider.inspectEffectiveSkill(projectPaths, "packaged-only")
	if err != nil {
		t.Fatal(err)
	}
	if packaged.Item.Scope != "packaged" || !strings.Contains(packaged.Item.Content, "# packaged-only") {
		t.Fatalf("packaged detail = %#v", packaged)
	}
}

func TestSkillRevisionTracksResourcesAndInvalidSiblingIsIsolated(t *testing.T) {
	projectRoot := t.TempDir()
	dir := writeSkill(t, projectRoot, "review-skill", "Use for reviews.", "v1")
	broken := filepath.Join(projectRoot, ".agents", "skills", "broken")
	if err := os.MkdirAll(broken, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "SKILL.md"), []byte("---\nname: broken\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	projectPaths, err := policy.New(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer projectPaths.Close()

	provider := &Provider{}
	first, err := provider.listSkills(projectPaths)
	if err != nil {
		t.Fatal(err)
	}
	if first.Complete || len(first.Items) != 1 || len(first.Diagnostics) != 1 {
		t.Fatalf("first catalog = %#v", first)
	}
	revision := first.Items[0].Revision
	if err := os.WriteFile(filepath.Join(dir, "references", "guide.md"), []byte("v2"), 0644); err != nil {
		t.Fatal(err)
	}
	second, err := provider.listSkills(projectPaths)
	if err != nil {
		t.Fatal(err)
	}
	if second.Items[0].Revision == revision {
		t.Fatal("resource change did not change Skill revision")
	}
	contentRevision := second.Items[0].Revision
	if err := os.Chmod(filepath.Join(dir, "references", "guide.md"), 0755); err != nil {
		t.Fatal(err)
	}
	third, err := provider.listSkills(projectPaths)
	if err != nil {
		t.Fatal(err)
	}
	if third.Items[0].Revision == contentRevision {
		t.Fatal("resource executable-bit change did not change Skill revision")
	}
}

func TestSkillDiscoveryRejectsSymlinkResources(t *testing.T) {
	projectRoot := t.TempDir()
	dir := writeSkill(t, projectRoot, "safe-skill", "Use safely.", "")
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "references", "link.md")); err != nil {
		t.Fatal(err)
	}
	projectPaths, err := policy.New(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer projectPaths.Close()

	provider := &Provider{}
	catalog, err := provider.listSkills(projectPaths)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Complete || len(catalog.Items) != 0 || len(catalog.Diagnostics) != 1 || catalog.Diagnostics[0].Code != "unsupported_symlink" {
		t.Fatalf("catalog = %#v", catalog)
	}
}

func TestSkillDiscoveryStopsAtAggregateCatalogBudget(t *testing.T) {
	projectRoot := t.TempDir()
	userHome := t.TempDir()
	packagedRoot := t.TempDir()
	writeSkill(t, projectRoot, "project-skill", "Project skill.", "")
	writeSkill(t, userHome, "user-skill", "User skill.", "")
	writePackagedSkill(t, packagedRoot, "packaged-skill", "Packaged skill.", "")

	projectPaths, err := policy.New(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer projectPaths.Close()

	provider := &Provider{UserHome: userHome, PackagedSkills: packagedRoot}
	catalog, _, cleanup := provider.discoverSkillsWithBudget(projectPaths, &skillCatalogBudget{maxFiles: 2, maxBytes: 1 << 20})
	defer cleanup()

	if catalog.Complete || len(catalog.Items) != 2 ||
		catalog.Items[0].Name != "project-skill" || catalog.Items[1].Name != "user-skill" {
		t.Fatalf("budgeted catalog = %#v", catalog)
	}
	foundBudget := false
	for _, diagnostic := range catalog.Diagnostics {
		if diagnostic.Scope == "packaged" && diagnostic.Code == "catalog_budget_exceeded" {
			foundBudget = true
		}
	}
	if !foundBudget {
		t.Fatalf("budget diagnostic missing: %#v", catalog.Diagnostics)
	}
}

func TestSkillDiscoveryRejectsMalformedSkillTrees(t *testing.T) {
	tests := []struct {
		name string
		code string
		make func(*testing.T, string)
	}{
		{
			name: "duplicate frontmatter",
			code: "duplicate_field",
			make: func(t *testing.T, root string) {
				dir := filepath.Join(root, ".agents", "skills", "duplicate")
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: duplicate\nname: duplicate\ndescription: duplicate\n---\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "name mismatch",
			code: "name_mismatch",
			make: func(t *testing.T, root string) {
				dir := filepath.Join(root, ".agents", "skills", "directory-name")
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: other-name\ndescription: mismatch\n---\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "invalid utf8",
			code: "invalid_utf8",
			make: func(t *testing.T, root string) {
				dir := filepath.Join(root, ".agents", "skills", "invalid-utf8")
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte{0xff, 0xfe}, 0644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "oversized skill file",
			code: "file_too_large",
			make: func(t *testing.T, root string) {
				dir := writeSkill(t, root, "oversized-skill", "Oversized skill.", "")
				if err := os.Truncate(filepath.Join(dir, "SKILL.md"), maxSkillBytes+1); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "oversized resource",
			code: "file_too_large",
			make: func(t *testing.T, root string) {
				dir := writeSkill(t, root, "oversized-resource", "Oversized resource.", "seed")
				if err := os.Truncate(filepath.Join(dir, "references", "guide.md"), maxResourceBytes+1); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "too many files",
			code: "too_many_files",
			make: func(t *testing.T, root string) {
				dir := writeSkill(t, root, "many-files", "Many files.", "")
				for index := 0; index < maxSkillFiles+1; index++ {
					path := filepath.Join(dir, "references", strings.Repeat("x", 4)+string(rune('a'+index%26))+"-"+strings.Repeat("0", index/26)+".txt")
					if err := os.WriteFile(path, nil, 0644); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
		{
			name: "too deep",
			code: "skill_too_deep",
			make: func(t *testing.T, root string) {
				dir := writeSkill(t, root, "deep-skill", "Deep skill.", "")
				current := filepath.Join(dir, "references")
				for index := 0; index < maxSkillDepth+1; index++ {
					current = filepath.Join(current, "d")
					if err := os.Mkdir(current, 0755); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.make(t, root)
			paths, err := policy.New(root)
			if err != nil {
				t.Fatal(err)
			}
			defer paths.Close()

			catalog, err := (&Provider{}).listSkills(paths)
			if err != nil {
				t.Fatal(err)
			}
			if catalog.Complete || len(catalog.Items) != 0 || len(catalog.Diagnostics) != 1 ||
				catalog.Diagnostics[0].Code != test.code {
				t.Fatalf("catalog = %#v", catalog)
			}
		})
	}
}

func TestSkillDiscoveryIsolatesUnavailableLowerPrioritySources(t *testing.T) {
	projectRoot := t.TempDir()
	writeSkill(t, projectRoot, "project-skill", "Project skill.", "")
	packagedRoot := t.TempDir()
	writePackagedSkill(t, packagedRoot, "packaged-skill", "Packaged skill.", "")

	projectPaths, err := policy.New(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer projectPaths.Close()

	provider := &Provider{
		UserHome:       filepath.Join(t.TempDir(), "missing-user-home"),
		PackagedSkills: packagedRoot,
	}
	catalog, err := provider.listSkills(projectPaths)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Complete || len(catalog.Items) != 2 ||
		catalog.Items[0].Name != "packaged-skill" || catalog.Items[1].Name != "project-skill" {
		t.Fatalf("catalog = %#v", catalog)
	}
	found := false
	for _, diagnostic := range catalog.Diagnostics {
		if diagnostic.Scope == "user" && diagnostic.Code == "user_registry_unavailable" {
			found = true
		}
	}
	if !found {
		t.Fatalf("user registry diagnostic missing: %#v", catalog.Diagnostics)
	}
}
