package windows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
)

type fakeLiveReplicaSource struct {
	status   OperatorStatus
	material ConnectionMaterial
	err      error
}

func (source fakeLiveReplicaSource) Status(context.Context, string) (OperatorStatus, error) {
	if source.err != nil {
		return OperatorStatus{}, source.err
	}
	return source.status, nil
}

func (source fakeLiveReplicaSource) Connection(context.Context, string) (ConnectionMaterial, error) {
	if source.err != nil {
		return ConnectionMaterial{}, source.err
	}
	return source.material, nil
}

type fakeReplicaStore struct {
	current ReplicaSnapshot
	publish int
	last    ConnectionMaterial
	err     error
}

func (store *fakeReplicaStore) Read(ExpectedInstallation) (ReplicaSnapshot, error) {
	return store.current, store.err
}

func (store *fakeReplicaStore) Publish(_ context.Context, _ ExpectedInstallation, material ConnectionMaterial) error {
	if store.err != nil {
		return store.err
	}
	store.publish++
	store.last = material
	return nil
}

type fakeReconciler struct {
	calls int
	err   error
}

func (reconciler *fakeReconciler) ReconcileEnabled(context.Context, string) error {
	reconciler.calls++
	return reconciler.err
}

func TestReplicaSynchronizerPublishesAndReconcilesOnlyOnEffectiveChange(t *testing.T) {
	expected := ExpectedInstallation{Distribution: "loki-mcp"}
	material := ConnectionMaterial{
		LocalOrigin: "http://127.0.0.1:18765/mcp", Transport: "streamable-http",
		Reachability: "loopback", AuthenticationType: "bearer-token-file", Token: "secret",
	}
	source := fakeLiveReplicaSource{
		status:   OperatorStatus{SchemaVersion: 1, State: "installed", Release: "1.2.3"},
		material: material,
	}
	store := &fakeReplicaStore{}
	reconciler := &fakeReconciler{}
	syncer := ReplicaSynchronizer{Source: source, Store: store, Reconciler: reconciler}

	result, err := syncer.Sync(t.Context(), expected)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || store.publish != 1 || reconciler.calls != 1 {
		t.Fatalf("result=%+v publish=%d reconcile=%d", result, store.publish, reconciler.calls)
	}

	sum := sha256.Sum256([]byte(material.Token))
	store.current = ReplicaSnapshot{
		Present: true, LocalOrigin: material.LocalOrigin, TokenSHA256: hex.EncodeToString(sum[:]),
	}
	result, err = syncer.Sync(t.Context(), expected)
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed || store.publish != 2 || reconciler.calls != 1 {
		t.Fatalf("result=%+v publish=%d reconcile=%d", result, store.publish, reconciler.calls)
	}
}

func TestReplicaSynchronizerDoesNotReconcileAfterPublishFailure(t *testing.T) {
	store := &fakeReplicaStore{err: errors.New("write failed")}
	reconciler := &fakeReconciler{}
	syncer := ReplicaSynchronizer{
		Source: fakeLiveReplicaSource{
			status: OperatorStatus{SchemaVersion: 1, State: "installed"},
			material: ConnectionMaterial{
				LocalOrigin: "http://127.0.0.1:18765/mcp", Transport: "streamable-http",
				Reachability: "loopback", AuthenticationType: "bearer-token-file", Token: "secret",
			},
		},
		Store: store, Reconciler: reconciler,
	}
	if _, err := syncer.Sync(t.Context(), ExpectedInstallation{Distribution: "loki-mcp"}); err == nil {
		t.Fatal("publish failure ignored")
	}
	if reconciler.calls != 0 {
		t.Fatalf("reconcile calls=%d", reconciler.calls)
	}
}

func TestParseLiveConnectionRejectsUnsupportedSchema(t *testing.T) {
	if _, err := parseLiveConnection([]byte(`{"schema_version":2,"local_origin":{"url":"http://127.0.0.1:18765/mcp","transport":"streamable-http","reachability":"loopback","authentication":{"type":"bearer-token-file"}}}`)); err == nil {
		t.Fatal("unsupported schema accepted")
	}
}

func TestBuildReplicaSnapshotAcceptsCurrentAndFlatLegacyState(t *testing.T) {
	expected := ExpectedInstallation{
		Distribution: "loki-mcp",
		StateDir:     `C:\Users\alice\AppData\Local\Loki\loki-mcp`,
	}
	material := ConnectionMaterial{
		LocalOrigin:        "http://127.0.0.1:18765/mcp",
		Transport:          "streamable-http",
		Reachability:       "loopback",
		AuthenticationType: "bearer-token-file",
		Token:              "secret-token",
	}
	current, err := BuildPublicConnection(expected, material)
	if err != nil {
		t.Fatal(err)
	}
	state := WindowsState{Present: true, Owned: true, Kind: WindowsStateManifest, MCPPort: 18765}
	for name, raw := range map[string][]byte{
		"current": current,
		"flat-legacy": []byte(fmt.Sprintf(
			`{"endpoint":"http://127.0.0.1:18765/mcp","transport":"streamable-http","authentication":"bearer-token-file","token_file":%q,"distribution":"loki-mcp"}`,
			joinWindowsPath(expected.StateDir, "mcp-token"),
		)),
	} {
		t.Run(name, func(t *testing.T) {
			snapshot, err := BuildReplicaSnapshot(expected, state, raw, []byte(material.Token))
			if err != nil {
				t.Fatal(err)
			}
			if !snapshot.Present || snapshot.LocalOrigin != material.LocalOrigin || snapshot.TokenSHA256 == "" {
				t.Fatalf("snapshot=%+v", snapshot)
			}
		})
	}
}

func TestBuildReplicaSnapshotRejectsOwnershipPortDrift(t *testing.T) {
	expected := ExpectedInstallation{
		Distribution: "loki-mcp",
		StateDir:     `C:\Users\alice\AppData\Local\Loki\loki-mcp`,
	}
	raw := []byte(fmt.Sprintf(
		`{"endpoint":"http://127.0.0.1:18765/mcp","transport":"streamable-http","authentication":"bearer-token-file","token_file":%q,"distribution":"loki-mcp"}`,
		joinWindowsPath(expected.StateDir, "mcp-token"),
	))
	_, err := BuildReplicaSnapshot(expected, WindowsState{MCPPort: 19000}, raw, []byte("secret-token"))
	if err == nil {
		t.Fatal("ownership/connection port drift accepted")
	}
}
