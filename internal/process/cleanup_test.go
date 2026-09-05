package process

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"loki/internal/fault"
)

func TestCleanupLifetimeAndOwnership(t *testing.T) {
	m := testManager(t, 1)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var calls atomic.Int32
	key := "cleanup-fixture"
	spec := StartSpec{Name: "cleanup", InstanceKey: &key, Spec: Spec{Argv: []string{"/usr/bin/true"}, CWD: t.TempDir(), Timeout: time.Second, MaxOutput: 4096}, Cleanup: func() error {
		calls.Add(1)
		close(entered)
		<-release
		return fault.Error("fixture cleanup warning")
	}}
	started, err := m.Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	id := started["session_id"].(string)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup not called")
	}
	value, err := m.Read(id, nil, 4096)
	if err != nil || value["status"] != "running" || value["exit_code"] != nil {
		t.Fatalf("cleanup advertised complete too soon: %#v %v", value, err)
	}
	var unused atomic.Bool
	spec.Cleanup = func() error { unused.Store(true); return nil }
	reused, err := m.Start(spec)
	if err != nil || reused["session_id"] != id || reused["reused"] != true {
		t.Fatalf("cleanup singleton not reused: %v", err)
	}
	spec.InstanceKey = nil
	if _, err := m.Start(spec); err == nil {
		t.Fatal("cleanup did not retain admission slot")
	}
	stopped := make(chan error, 1)
	go func() { _, err := m.Stop(id); stopped <- err }()
	select {
	case <-stopped:
		t.Fatal("Stop returned before cleanup")
	case <-time.After(20 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not finish")
	}
	value, err = m.Read(id, nil, 4096)
	if err != nil || value["status"] != "exited" || value["cleanup_error"] != "fixture cleanup warning" || calls.Load() != 1 || unused.Load() {
		t.Fatalf("cleanup ownership/result: %#v %v", value, err)
	}
	m.Close()
	if calls.Load() != 1 {
		t.Fatal("cleanup ran twice")
	}
}

func TestCleanupErrorDoesNotExposePrivateDetails(t *testing.T) {
	m := testManager(t, 1)
	r, err := m.Start(StartSpec{Name: "cleanup", Spec: Spec{Argv: []string{"/usr/bin/true"}, CWD: t.TempDir(), Timeout: time.Second, MaxOutput: 4096}, Cleanup: func() error { return errors.New("private-file-details") }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop(r["session_id"].(string)); err != nil {
		t.Fatal(err)
	}
	value, err := m.Read(r["session_id"].(string), nil, 4096)
	if err != nil || value["cleanup_error"] == nil || value["cleanup_error"] == "private-file-details" {
		t.Fatalf("cleanup error boundary: %#v %v", value, err)
	}
}
