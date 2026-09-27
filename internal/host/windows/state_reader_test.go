package windows

import (
	"encoding/json"
	"errors"
	"testing"
)

type fakeStateFilesystem struct {
	paths map[string]StatePath
	dirs  map[string][]string
	files map[string][]byte
	errs  map[string]error
}

func (filesystem fakeStateFilesystem) Lstat(path string) (StatePath, error) {
	if err := filesystem.errs["stat:"+path]; err != nil {
		return StatePath{}, err
	}
	return filesystem.paths[path], nil
}

func (filesystem fakeStateFilesystem) ReadDir(path string) ([]string, error) {
	if err := filesystem.errs["dir:"+path]; err != nil {
		return nil, err
	}
	return append([]string(nil), filesystem.dirs[path]...), nil
}

func (filesystem fakeStateFilesystem) ReadFile(path string) ([]byte, error) {
	if err := filesystem.errs["read:"+path]; err != nil {
		return nil, err
	}
	return append([]byte(nil), filesystem.files[path]...), nil
}

func manifestFixture(t *testing.T, expected ExpectedInstallation) []byte {
	t.Helper()
	raw, err := json.Marshal(ownershipManifestDisk{
		SchemaVersion:   1,
		Distribution:    expected.Distribution,
		ReleaseTag:      "v0.1.19",
		StateDir:        expected.StateDir,
		InstallLocation: `D:\Loki\loki-mcp`,
		AutoStart:       true,
		MCPPort:         18765,
		TaskName:        expected.TaskName,
		TaskExecutable:  expected.TaskExecutable,
		TaskArguments:   expected.TaskArguments,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestInspectWindowsStateManifest(t *testing.T) {
	expected := fixtureExpected()
	ownership := joinWindowsPath(expected.StateDir, "ownership.json")
	fs := fakeStateFilesystem{
		paths: map[string]StatePath{
			expected.StateDir:  {Exists: true, Directory: true},
			ownership:          {Exists: true, Regular: true},
			`D:\Loki\loki-mcp`: {Exists: true, Directory: true},
		},
		dirs: map[string][]string{
			expected.StateDir: {"mcp-token", "ownership.json", "connection.json"},
		},
		files: map[string][]byte{ownership: manifestFixture(t, expected)},
		errs:  map[string]error{},
	}
	state, err := InspectWindowsState(fs, expected)
	if err != nil {
		t.Fatal(err)
	}
	if state.Kind != WindowsStateManifest || !state.Owned || state.MCPPort != 18765 {
		t.Fatalf("unexpected manifest state %#v", state)
	}
}

func TestInspectWindowsStateLegacyFlat(t *testing.T) {
	expected := fixtureExpected()
	connection := joinWindowsPath(expected.StateDir, "connection.json")
	token := joinWindowsPath(expected.StateDir, "mcp-token")
	raw, _ := json.Marshal(map[string]any{
		"endpoint":       "http://127.0.0.1:18765/mcp",
		"transport":      "streamable-http",
		"authentication": "bearer-token-file",
		"token_file":     token,
		"distribution":   expected.Distribution,
	})
	fs := fakeStateFilesystem{
		paths: map[string]StatePath{
			expected.StateDir: {Exists: true, Directory: true},
			connection:        {Exists: true, Regular: true},
			token:             {Exists: true, Regular: true},
		},
		dirs:  map[string][]string{expected.StateDir: {"connection.json", "mcp-token"}},
		files: map[string][]byte{connection: raw},
		errs:  map[string]error{},
	}
	state, err := InspectWindowsState(fs, expected)
	if err != nil {
		t.Fatal(err)
	}
	if state.Kind != WindowsStateLegacy || !state.Owned {
		t.Fatalf("unexpected legacy state %#v", state)
	}
}

func TestInspectWindowsStateRejectsUnexpectedOrReparseState(t *testing.T) {
	expected := fixtureExpected()
	tests := []fakeStateFilesystem{
		{
			paths: map[string]StatePath{expected.StateDir: {Exists: true, Directory: true, Reparse: true}},
			dirs:  map[string][]string{}, files: map[string][]byte{}, errs: map[string]error{},
		},
		{
			paths: map[string]StatePath{expected.StateDir: {Exists: true, Directory: true}},
			dirs:  map[string][]string{expected.StateDir: {"foreign.txt"}},
			files: map[string][]byte{}, errs: map[string]error{},
		},
	}
	for index, fs := range tests {
		state, err := InspectWindowsState(fs, expected)
		if err != nil {
			t.Fatalf("case %d: %v", index, err)
		}
		if !state.Present || state.Owned || state.Kind != WindowsStateUnverified {
			t.Fatalf("case %d: unexpected state %#v", index, state)
		}
	}
}

func TestInspectWindowsStatePropagatesIOFailure(t *testing.T) {
	expected := fixtureExpected()
	fs := fakeStateFilesystem{
		paths: map[string]StatePath{expected.StateDir: {Exists: true, Directory: true}},
		dirs:  map[string][]string{},
		files: map[string][]byte{},
		errs:  map[string]error{"dir:" + expected.StateDir: errors.New("denied")},
	}
	if _, err := InspectWindowsState(fs, expected); err == nil {
		t.Fatal("I/O failure was treated as absent or unverified")
	}
}
