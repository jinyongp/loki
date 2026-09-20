package secret

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSecretMetadataPaginationAndRevision(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "vault")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	c := Controller{StateDirectory: root}
	if _, err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha", "beta", "gamma"} {
		if _, err := c.CreateProfile(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	first, err := c.ProfilesPage(ctx, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if first["revision"] != uint64(4) || first["offset"] != 0 || first["limit"] != 2 ||
		first["total"] != 3 || first["has_more"] != true || first["next_offset"] != 2 || first["complete"] != true {
		t.Fatalf("first profile page = %#v", first)
	}
	items := first["profiles"].([]map[string]any)
	if len(items) != 2 || items[0]["name"] != "alpha" || items[1]["name"] != "beta" {
		t.Fatalf("profile page items = %#v", items)
	}
	second, err := c.ProfilesPage(ctx, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if second["has_more"] != false || second["next_offset"] != nil || len(second["profiles"].([]map[string]any)) != 1 {
		t.Fatalf("second profile page = %#v", second)
	}
	profile, err := c.Profile(ctx, "beta")
	if err != nil {
		t.Fatal(err)
	}
	if profile["revision"] != uint64(4) || profile["name"] != "beta" {
		t.Fatalf("profile metadata = %#v", profile)
	}
	if _, err = c.ProfilesPage(ctx, -1, 1); err == nil {
		t.Fatal("negative profile offset accepted")
	}
	if _, err = c.ProfilesPage(ctx, 0, MaxProfiles+1); err == nil {
		t.Fatal("oversized profile page accepted")
	}
}

func TestSecretImportMetadataPagination(t *testing.T) {
	root := t.TempDir()
	inbox := filepath.Join(root, "inbox")
	if err := os.Mkdir(inbox, 0o700); err != nil {
		t.Fatal(err)
	}
	c := Controller{StateDirectory: root, InboxDirectory: inbox}
	base := time.Unix(1_700_000_000, 0)
	ids := []string{
		"00000000000000000000000000000001",
		"00000000000000000000000000000002",
		"00000000000000000000000000000003",
	}
	for index, id := range ids {
		path := filepath.Join(inbox, id+".env")
		if err := os.WriteFile(path, []byte("TOKEN=value\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := base.Add(time.Duration(index) * time.Second)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	page, err := c.ListImportsPage(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if page["offset"] != 1 || page["limit"] != 1 || page["total"] != 3 ||
		page["has_more"] != true || page["next_offset"] != 2 || page["complete"] != true {
		t.Fatalf("import page = %#v", page)
	}
	items := page["imports"].([]map[string]any)
	if len(items) != 1 || items[0]["import_id"] != ids[1] || items[0]["bytes"] != int64(len("TOKEN=value\n")) {
		t.Fatalf("import page items = %#v", items)
	}
	if _, err = c.ListImportsPage(0, 201); err == nil {
		t.Fatal("oversized import page accepted")
	}
}
