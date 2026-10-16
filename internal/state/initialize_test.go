package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	"loki/internal/platform/safeio"
)

func TestInitializationIOFailureRetry(t *testing.T) {
	const fixtureEnv = "LOKI_STATE_INIT_FAULT_DIR"
	if dir := os.Getenv(fixtureEnv); dir != "" {
		// Limit only this child. A key fits, but the intent/store cannot be
		// written; no mocked filesystem or production publication code is used.
		signal.Ignore(unix.SIGXFSZ)
		if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &unix.Rlimit{Cur: 32, Max: 32}); err != nil {
			t.Fatal(err)
		}
		if _, err := (Store{Dir: dir}).Initialize(t.Context(), json.RawMessage(`{"value":"original"}`)); !errors.Is(err, unix.EFBIG) {
			t.Fatalf("expected real file-size write failure: %v", err)
		}
		return
	}
	dir := filepath.Join(t.TempDir(), "vault")
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestInitializationIOFailureRetry$")
	cmd.Env = append(os.Environ(), fixtureEnv+"="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fault fixture: %v %s", err, output)
	}
	for _, name := range []string{initializationFile, "master.key", "store.json"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("write failure left active initialization file %s: %v", name, err)
		}
	}
	s := Store{Dir: dir}
	data := json.RawMessage(`{"value":"retry"}`)
	if _, err := s.Initialize(t.Context(), data); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Load(t.Context()); err != nil || !bytes.Equal(got.Data, data) {
		t.Fatalf("retry after I/O failure: %+v %v", got, err)
	}
}

func TestInitializationPublicationRecovery(t *testing.T) {
	for _, name := range []string{initializationFile, "master.key", "store.json"} {
		for _, after := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/before", true: "/after"}[after], func(t *testing.T) {
				s := Store{Dir: filepath.Join(t.TempDir(), "vault")}
				if err := safeDirectory(s.Dir); err != nil {
					t.Fatal(err)
				}
				injected := errors.New("publication interrupted")
				publish := func(path string, data []byte, overwrite bool) error {
					if filepath.Base(path) == name && !after {
						return injected
					}
					if err := safeio.PublishPrivate(path, data, overwrite); err != nil {
						return err
					}
					if filepath.Base(path) == name {
						return injected
					}
					return nil
				}
				original := json.RawMessage(`{"value":"original","large":9007199254740993}`)
				if _, err := s.initialize(t.Context(), original, publish); !errors.Is(err, injected) {
					t.Fatalf("fault did not trigger: %v", err)
				}
				keyBefore, keyErr := os.ReadFile(filepath.Join(s.Dir, "master.key"))
				_, intentErr := os.Lstat(filepath.Join(s.Dir, initializationFile))
				wantErr := ErrInitializationPending
				retryData := json.RawMessage(`{"value":"retry"}`)
				if errors.Is(intentErr, os.ErrNotExist) {
					wantErr = ErrUninitialized
					retryData = original
				}
				if _, err := s.Load(t.Context()); !errors.Is(err, wantErr) {
					t.Fatalf("read before recovery: %v, want %v", err, wantErr)
				}
				if _, err := s.Update(t.Context(), nil, func(json.RawMessage) (json.RawMessage, error) {
					t.Fatal("mutation ran before initialization completed")
					return nil, nil
				}); !errors.Is(err, wantErr) {
					t.Fatalf("write before recovery: %v", err)
				}
				// A new instance, with different retry input, must finish the
				// original prepared snapshot instead of silently replacing it.
				reopened := Store{Dir: s.Dir}
				if created, err := reopened.Initialize(t.Context(), retryData); err != nil || !created {
					t.Fatalf("retry: created=%t err=%v", created, err)
				}
				snapshot, err := reopened.Load(t.Context())
				if err != nil || snapshot.Revision != 1 || !bytes.Equal(snapshot.Data, original) {
					t.Fatalf("recovered snapshot: %+v %v", snapshot, err)
				}
				if keyErr == nil {
					keyAfter, err := os.ReadFile(filepath.Join(s.Dir, "master.key"))
					if err != nil || !bytes.Equal(keyBefore, keyAfter) {
						t.Fatal("recovery replaced the original key", err)
					}
				}
				if _, err := os.Lstat(filepath.Join(s.Dir, initializationFile)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("recovery record not removed: %v", err)
				}
				if _, err := reopened.Update(t.Context(), nil, func(json.RawMessage) (json.RawMessage, error) {
					return json.RawMessage(`{"value":"updated"}`), nil
				}); err != nil {
					t.Fatal(err)
				}
				if created, err := reopened.Initialize(t.Context(), original); err != nil || created {
					t.Fatalf("reinitialization: created=%t err=%v", created, err)
				}
				if snapshot, err := reopened.Load(t.Context()); err != nil || snapshot.Revision != 2 || string(snapshot.Data) != `{"value":"updated"}` {
					t.Fatalf("reinitialization lost updated state: %+v %v", snapshot, err)
				}
			})
		}
	}
}

func interruptedInitialization(t *testing.T) Store {
	t.Helper()
	s := Store{Dir: filepath.Join(t.TempDir(), "vault")}
	if err := safeDirectory(s.Dir); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	publish := func(path string, data []byte, overwrite bool) error {
		if err := safeio.PublishPrivate(path, data, overwrite); err != nil {
			return err
		}
		if filepath.Base(path) == "master.key" {
			cancel()
		}
		return nil
	}
	if _, err := s.initialize(ctx, json.RawMessage(`{"value":"original"}`), publish); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation did not interrupt initialization: %v", err)
	}
	return s
}

func TestInitializationCancellationRecovery(t *testing.T) {
	s := interruptedInitialization(t)
	if _, err := s.Initialize(t.Context(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Load(t.Context()); err != nil || string(got.Data) != `{"value":"original"}` {
		t.Fatalf("cancel recovery: %+v %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	fresh := Store{Dir: filepath.Join(t.TempDir(), "fresh")}
	if _, err := fresh.Initialize(ctx, json.RawMessage(`{}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled initialization: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fresh.Dir, initializationFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled request created intent: %v", err)
	}
}

func TestInitializationRecoveryRejectsConflictingFiles(t *testing.T) {
	for _, kind := range []string{"key", "store", "store-without-key", "intent", "key-symlink", "store-symlink", "intent-symlink", "intent-permissions"} {
		t.Run(kind, func(t *testing.T) {
			s := interruptedInitialization(t)
			switch kind {
			case "key":
				if err := os.WriteFile(filepath.Join(s.Dir, "master.key"), bytes.Repeat([]byte{1}, 32), 0600); err != nil {
					t.Fatal(err)
				}
			case "store", "store-without-key":
				if kind == "store-without-key" {
					if err := os.Remove(filepath.Join(s.Dir, "master.key")); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(s.Dir, "store.json"), []byte("unrelated"), 0600); err != nil {
					t.Fatal(err)
				}
			case "intent":
				if err := os.WriteFile(filepath.Join(s.Dir, initializationFile), []byte(`{"schema":1}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "intent-permissions":
				if err := os.Chmod(filepath.Join(s.Dir, initializationFile), 0644); err != nil {
					t.Fatal(err)
				}
			default:
				name := map[string]string{"key-symlink": "master.key", "store-symlink": "store.json", "intent-symlink": initializationFile}[kind]
				path := filepath.Join(s.Dir, name)
				if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), path); err != nil {
					t.Fatal(err)
				}
			}
			before := directoryContents(t, s.Dir)
			if _, err := s.Initialize(t.Context(), json.RawMessage(`{}`)); err == nil {
				t.Fatal("accepted conflicting recovery files")
			}
			if got := directoryContents(t, s.Dir); !bytes.Equal(before, got) {
				t.Fatal("failed recovery changed existing files")
			}
		})
	}
}

// Capture names, modes, link targets and private file bytes without following
// links. No file content is written to test diagnostics.
func directoryContents(t *testing.T, dir string) []byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var result bytes.Buffer
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		result.WriteString(entry.Name() + ":" + info.Mode().String() + ":")
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(filepath.Join(dir, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			result.WriteString(link)
		} else {
			data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			result.Write(data)
		}
		result.WriteByte('\n')
	}
	return result.Bytes()
}

func TestInitializationWithoutIntentPreservesPartialStore(t *testing.T) {
	for _, name := range []string{"master.key", "store.json"} {
		t.Run(name, func(t *testing.T) {
			s := Store{Dir: t.TempDir()}
			if err := os.Chmod(s.Dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(s.Dir, name)
			if err := os.WriteFile(path, []byte("existing"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Initialize(t.Context(), json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "without a recovery record") {
				t.Fatalf("unexplained partial state: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "existing" {
				t.Fatal("replaced an unexplained partial store")
			}
			if _, err := os.Stat(filepath.Join(s.Dir, initializationFile)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invented an initialization intent: %v", err)
			}
		})
	}
}

func TestInitializationStaleIntentCannotRollBackUpdates(t *testing.T) {
	s := interruptedInitialization(t)
	intent, err := os.ReadFile(filepath.Join(s.Dir, initializationFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Initialize(t.Context(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	updated := json.RawMessage(`{"value":"new committed data"}`)
	if _, err := s.Update(t.Context(), nil, func(json.RawMessage) (json.RawMessage, error) { return updated, nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, initializationFile), intent, 0600); err != nil {
		t.Fatal(err)
	}
	before := directoryContents(t, s.Dir)
	if _, err := s.Initialize(t.Context(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("accepted stale initialization intent")
	}
	if !bytes.Equal(before, directoryContents(t, s.Dir)) {
		t.Fatal("stale initialization intent changed committed data")
	}
}
