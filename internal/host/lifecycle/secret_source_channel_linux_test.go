//go:build linux

package lifecycle

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"loki/internal/control/appvalues"
	"loki/internal/platform/privateio"
)

func TestSourceBackupEventsRequireOriginalProofBeforeMigration(t *testing.T) {
	root := privateLifecycleRoot(t)
	lock, err := AcquireOperationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	j, err := OpenOperationJournal(root, lock, OperationJournalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	intent := migrationIntentFixture(t, root)
	r, err := j.BeginSecretMigration(intent, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	operation := MigrationOperationUUID(r.ID)
	checkpoint := func(kind string, proof *appvalues.SourceBackupProof) error {
		return j.CheckpointMigrationSourceEvent(r.ID, SourceEvent{Operation: operation, Type: kind, Archive: proof}, time.Now())
	}
	if err := checkpoint("source_frozen", nil); err != nil {
		t.Fatal(err)
	}
	if checkpoint("source_backup_complete", nil) == nil {
		t.Fatal("target writes allowed before durable encrypted backup")
	}
	proof := appvalues.SourceBackupProof{Version: 1, Operation: operation, Revision: intent.Plan.Revision, Recipient: intent.Recipient, CipherSHA256: strings.Repeat("e", 64), Bytes: 4096}
	wrong := proof
	wrong.Revision++
	if checkpoint("archive_prepared", &wrong) == nil || checkpoint("archive_published", &proof) == nil {
		t.Fatal("foreign source proof or publication without preparation accepted")
	}
	if err := checkpoint("archive_prepared", &proof); err != nil {
		t.Fatal(err)
	}
	if checkpoint("source_backup_complete", nil) == nil {
		t.Fatal("prepared but unpublished archive allowed target writes")
	}
	if err := checkpoint("archive_published", &proof); err != nil {
		t.Fatal(err)
	}
	if err := checkpoint("archive_published", &proof); err != nil {
		t.Fatal("same publication ACK recovery", err)
	}
	if err := checkpoint("source_backup_complete", nil); err != nil {
		t.Fatal(err)
	}
	if checkpoint("source_frozen", nil) == nil || checkpoint("archive_prepared", &proof) == nil {
		t.Fatal("source backup restarted after target writes became possible")
	}
}

func TestSourceChannelRejectsForeignOrMalformedEventsBeforeHostEffects(t *testing.T) {
	operation := "123e4567-e89b-42d3-a456-426614174551"
	for _, test := range []struct {
		name  string
		frame any
	}{
		{"foreign operation", sourceEnvelope{Version: 1, Sequence: 1, Event: SourceEvent{Operation: "123e4567-e89b-42d3-a456-426614174552", Type: "target_sealed"}}},
		{"wrong sequence", sourceEnvelope{Version: 1, Sequence: 2, Event: SourceEvent{Operation: operation, Type: "target_sealed"}}},
		{"unknown version", sourceEnvelope{Version: 2, Sequence: 1, Event: SourceEvent{Operation: operation, Type: "target_sealed"}}},
		{"unknown value field", map[string]any{"version": 1, "sequence": 1, "event": map[string]any{"operation": operation, "type": "target_sealed", "value": "must-not-be-a-host-field"}}},
		{"null pointer field", map[string]any{"version": 1, "sequence": 1, "event": map[string]any{"operation": operation, "type": "target_sealed", "write": nil}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			host, producer := net.Pipe()
			done := make(chan struct{})
			go func() { defer close(done); defer producer.Close(); _ = privateio.WriteControl(producer, test.frame) }()
			called := false
			if err := CheckpointSourceTransfer(ctx, host, operation, func(SourceEvent) error { called = true; return nil }); err == nil || called {
				t.Fatal("malformed source event reached canonical host checkpoint", err)
			}
			<-done
		})
	}
}

func TestSourceChannelAcknowledgesExactDurableMetadataFrame(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	operation := "123e4567-e89b-42d3-a456-426614174553"
	host, producer := net.Pipe()
	defer producer.Close()
	frame, _ := json.Marshal(sourceEnvelope{Version: 1, Sequence: 1, Event: SourceEvent{Operation: operation, Type: "target_sealed"}})
	done := make(chan error, 1)
	durable := false
	go func() {
		done <- CheckpointSourceTransfer(ctx, host, operation, func(SourceEvent) error { durable = true; return nil })
	}()
	if err := privateio.WriteBytes(producer, frame, privateio.MaxControlFrame); err != nil {
		t.Fatal(err)
	}
	var ack sourceAcknowledgement
	if err := privateio.ReadControl(producer, &ack); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil || !durable || !ack.Accepted || ack.Version != 1 || ack.Sequence != 1 || ack.Operation != operation || ack.Digest != sourceEventDigest(frame) {
		t.Fatal("ACK did not bind the durable original metadata", err)
	}
}
