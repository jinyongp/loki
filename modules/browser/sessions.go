package browser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"loki/internal/platform/filelock"
)

type sessionMarker struct {
	Schema         int    `json:"schema"`
	Session        string `json:"session"`
	EnginePrepared bool   `json:"engine_prepared,omitempty"`
}

// Persistent external lease files identify live profiles even after the
// manager crashes. Reclamation also requires proof that the engine was never
// prepared: a lost manager lease alone cannot prove all Chrome children exited.
// Results, unknown directories and uncertain engine profiles are retained.
func ownedSession(ctx context.Context, data, project, engine string) (profile, output string, cleanup func(), err error) {
	if err := os.MkdirAll(data, 0700); err != nil {
		return "", "", nil, err
	}
	data, err = filepath.EvalSymlinks(data)
	if err != nil {
		return "", "", nil, err
	}
	root, err := os.OpenRoot(data)
	if err != nil {
		return "", "", nil, err
	}
	defer root.Close()
	profiles := filepath.Join("profiles", project, engine)
	leases := filepath.Join("leases", project, engine)
	for _, directory := range []string{profiles, leases} {
		if err := root.MkdirAll(directory, 0700); err != nil {
			return "", "", nil, err
		}
	}
	mutation, err := root.OpenFile(filepath.Join(leases, "mutations.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", "", nil, err
	}
	defer mutation.Close()
	var unlock func()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		unlock, err = filelock.Exclusive(mutation)
		if err == nil {
			break
		}
		if !filelock.Busy(err) {
			return "", "", nil, err
		}
		select {
		case <-ctx.Done():
			return "", "", nil, ctx.Err()
		case <-deadline.C:
			return "", "", nil, fmt.Errorf("browser profile lifecycle is busy; retry the connection")
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer unlock()
	if err := reclaimProfiles(root, profiles, leases); err != nil {
		return "", "", nil, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", "", nil, err
	}
	session := "session-" + hex.EncodeToString(random[:])
	lease, err := root.OpenFile(filepath.Join(leases, session+".lock"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return "", "", nil, err
	}
	release, err := filelock.Exclusive(lease)
	if err != nil {
		lease.Close()
		return "", "", nil, err
	}
	profileRelative := filepath.Join(profiles, session)
	if err := root.Mkdir(profileRelative, 0700); err != nil {
		release()
		lease.Close()
		return "", "", nil, err
	}
	profile = filepath.Join(data, profileRelative)
	cleanupRoot, err := os.OpenRoot(data)
	if err != nil {
		_ = root.RemoveAll(profileRelative)
		release()
		lease.Close()
		return "", "", nil, err
	}
	var once sync.Once
	cleanup = func() {
		once.Do(func() {
			_ = cleanupRoot.RemoveAll(profileRelative)
			_ = cleanupRoot.Close()
			release()
			_ = lease.Close()
		})
	}
	marker, _ := json.Marshal(sessionMarker{Schema: 1, Session: session})
	if err := root.WriteFile(filepath.Join(profileRelative, ".loki-browser-session.json"), marker, 0600); err != nil {
		cleanup()
		return "", "", nil, err
	}
	outputRelative := filepath.Join("results", project, engine, session)
	if err := root.MkdirAll(outputRelative, 0700); err != nil {
		cleanup()
		return "", "", nil, err
	}
	return profile, filepath.Join(data, outputRelative), cleanup, nil
}

func reclaimProfiles(root *os.Root, profiles, leases string) error {
	directory, err := root.Open(profiles)
	if err != nil {
		return err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(4097)
	if err != nil && err != io.EOF {
		return err
	}
	if len(entries) > 4096 {
		return fmt.Errorf("browser profile count exceeds lifecycle bound")
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !strings.HasPrefix(name, "session-") || len(name) != 40 {
			continue
		}
		if _, err := hex.DecodeString(strings.TrimPrefix(name, "session-")); err != nil {
			continue
		}
		markerPath := filepath.Join(profiles, name, ".loki-browser-session.json")
		info, err := root.Lstat(markerPath)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 512 {
			continue
		}
		contents, err := root.ReadFile(markerPath)
		var marker sessionMarker
		if err != nil || json.Unmarshal(contents, &marker) != nil || marker.Schema != 1 || marker.Session != name || marker.EnginePrepared {
			continue
		}
		leasePath := filepath.Join(leases, name+".lock")
		info, err = root.Lstat(leasePath)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		lease, err := root.OpenFile(leasePath, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		release, err := filelock.Exclusive(lease)
		if err != nil {
			lease.Close()
			if filelock.Busy(err) {
				continue
			}
			return err
		}
		err = root.RemoveAll(filepath.Join(profiles, name))
		release()
		lease.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func markEnginePrepared(profile string) error {
	root, err := os.OpenRoot(profile)
	if err != nil {
		return err
	}
	defer root.Close()
	data, err := json.Marshal(sessionMarker{Schema: 1, Session: filepath.Base(profile), EnginePrepared: true})
	if err != nil {
		return err
	}
	return root.WriteFile(".loki-browser-session.json", data, 0600)
}
