package workspace

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestBundleConfinementAndLimits(t *testing.T) {
	f := fixture(t)
	root := f.Policy.Root()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"a.txt": "alpha", "nested/b.txt": "beta", "nested/.env": "hidden"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	data, meta, err := f.Bundle(t.Context(), []string{".", "a.txt"}, "bundle.zip")
	if err != nil {
		t.Fatal(err)
	}
	if meta["file_count"] != 2 || meta["input_bytes"] != 9 || meta["excluded_entries"] != 2 {
		t.Fatal(meta)
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"alpha", "beta"} {
		file, err := z.File[i].Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(file)
		file.Close()
		if err != nil || string(content) != want {
			t.Fatalf("%q %v", content, err)
		}
	}
	for _, path := range []string{"../escape", "link", "nested/.env"} {
		if _, _, err := f.Bundle(t.Context(), []string{path}, "x.zip"); err == nil {
			t.Fatal(path)
		}
	}
	if _, _, err := f.Bundle(t.Context(), []string{"."}, "../x.zip"); err == nil {
		t.Fatal("filename traversal")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := f.Bundle(ctx, []string{"."}, "x.zip"); err == nil {
		t.Fatal("cancellation")
	}
	large, err := os.Create(filepath.Join(root, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err = large.Truncate(MaxSharedBytes + 1); err != nil {
		t.Fatal(err)
	}
	large.Close()
	if _, err = f.Attachment("large.bin"); err == nil {
		t.Fatal("attachment limit")
	}
	if _, _, err = f.Bundle(t.Context(), []string{"large.bin"}, "x.zip"); err == nil {
		t.Fatal("bundle limit")
	}
}
