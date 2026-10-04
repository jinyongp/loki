package management

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestReplaceStateFileWaitsForReader(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "state.json")
	source := filepath.Join(directory, "pending.json")
	for path, contents := range map[string]string{destination: "old", source: "new"} {
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- replaceStateFile(source, destination) }()
	select {
	case err := <-result:
		windows.CloseHandle(handle)
		t.Fatalf("replacement completed while reader denied delete sharing: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "old" {
		windows.CloseHandle(handle)
		t.Fatalf("old state was not retained: %q, %v", data, err)
	}
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "new" {
		t.Fatalf("replacement state: %q, %v", data, err)
	}
}
