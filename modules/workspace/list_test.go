package workspace

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"testing"
	"testing/fstest"

	"loki/internal/fault"
)

func listTestDirectory(t *testing.T, names ...string) fs.ReadDirFile {
	t.Helper()
	files := fstest.MapFS{}
	for _, name := range names {
		files[name] = &fstest.MapFile{}
	}
	dir, err := files.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dir.Close() })
	return dir.(fs.ReadDirFile)
}

func TestListScanBudget(t *testing.T) {
	remaining := 3
	children, err := readListDirectory(t.Context(), listTestDirectory(t, "a", "b", "c"), &remaining)
	if err != nil || len(children) != 3 || remaining != 0 {
		t.Fatalf("exact scan budget: %v, %d remaining, %v", children, remaining, err)
	}
	// The same budget applies to subsequent directories, including names which
	// would later be filtered by policy. No incomplete sorted page is returned.
	children, err = readListDirectory(t.Context(), listTestDirectory(t, ".env"), &remaining)
	if children != nil || fault.Describe(err).Code != fault.CodeQuotaExceeded {
		t.Fatalf("request-wide scan budget: %v, %v", children, err)
	}
}

type cancelListDirectory struct {
	fs.ReadDirFile
	cancel context.CancelFunc
}

func (d cancelListDirectory) ReadDir(n int) ([]fs.DirEntry, error) {
	children, err := d.ReadDirFile.ReadDir(n)
	d.cancel()
	return children, err
}

func TestListCancellationDuringDirectoryRead(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	remaining := maxListScanEntries
	children, err := readListDirectory(ctx, cancelListDirectory{listTestDirectory(t, "a"), cancel}, &remaining)
	if !errors.Is(err, context.Canceled) || children != nil {
		t.Fatalf("canceled scan returned %v, %v", children, err)
	}
	f := fixture(t)
	if _, err := f.List(ctx, ".", 1, 0, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled list: %v", err)
	}
}

func TestListPagesPreserveTraversalOrder(t *testing.T) {
	f := fixture(t)
	for _, path := range []string{"b/z", "a/y", "z", "c"} {
		if _, err := f.Create(path, "content"); err != nil {
			t.Fatal(err)
		}
	}
	var paths []string
	for offset := 0; ; {
		page, err := f.List(t.Context(), ".", 3, offset, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range page["entries"].([]map[string]any) {
			paths = append(paths, entry["path"].(string))
		}
		if !page["has_more"].(bool) {
			break
		}
		offset = page["next_offset"].(int)
	}
	if want := []string{"a", "b", "c", "z", "a/y", "b/z"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("pages: %v, want %v", paths, want)
	}
}
