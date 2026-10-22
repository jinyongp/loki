package lifecycle

import (
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"time"

	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/platform/privateio"
)

const MaxSourceManifestBytes = 128 << 20

// SourceTransferManifest is public metadata for the owned helper namespace.
// Its type has no source value, key, passphrase or task-context field.
type SourceTransferManifest struct {
	Version         int                        `json:"version"`
	Operation       string                     `json:"operation"`
	Policy          string                     `json:"policy"`
	SourceRoot      string                     `json:"source_root"`
	TargetSocket    string                     `json:"target_socket"`
	TargetUID       uint32                     `json:"target_uid"`
	Plan            appvalues.MigrationPlan    `json:"plan"`
	BaseEnvironment map[string]string          `json:"base_environment"`
	Writes          []MigrationWriteCheckpoint `json:"writes"`
}

func (m SourceTransferManifest) Valid() bool {
	if m.Version != 1 || !controlpolicy.ApplicationUUID.MatchString(m.Operation) || !migrationHex(m.Policy, 32) || m.TargetUID == 0 || m.Plan.Version != 1 || m.Plan.Revision == 0 || m.Plan.Mappings == nil || m.Writes == nil || len(m.Plan.Resources) == 0 || len(m.TargetSocket) >= 108 {
		return false
	}
	for _, path := range []string{m.SourceRoot, m.TargetSocket} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || len(path) > 4096 || strings.ContainsAny(path, "\x00\r\n") {
			return false
		}
	}
	return true
}

func WriteSourceTransferManifest(channel io.Writer, manifest SourceTransferManifest) error {
	if !manifest.Valid() {
		return ErrSourceSession
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return ErrSourceSession
	}
	defer clear(encoded)
	return privateio.WriteBytes(channel, encoded, MaxSourceManifestBytes)
}

// CheckpointMigrationSourceEvent accepts no helper-selected operation or
// release. Every immutable write/delivery effect stays in the canonical journal.
func (j *OperationJournal) CheckpointMigrationSourceEvent(id string, event SourceEvent, now time.Time) error {
	r, found, err := j.Active()
	if err != nil || !found || r.ID != id || r.Kind != OperationSecretMigration || r.SecretMigration == nil || r.State != OperationApplying || r.Phase != PhaseMigrate || event.Operation != MigrationOperationUUID(id) {
		return ErrSourceSession
	}
	switch event.Type {
	case "target_sealed":
		if event.Write != nil || event.Binding != nil || event.Attempt != "" || event.Count != 0 {
			return ErrSourceSession
		}
		_, err = j.AdvanceSecretMigration(id, PhaseSwitch, now)
	case "write_prepared", "write_completed":
		if event.Write == nil || event.Binding != nil || event.Attempt != "" || event.Count != 0 || (event.Type == "write_completed") != event.Write.Complete {
			return ErrSourceSession
		}
		if event.Type == "write_prepared" {
			_, err = j.CheckpointMigrationWrite(id, *event.Write, now)
		} else {
			found := false
			for _, write := range r.SecretMigration.Writes {
				write.Complete = true
				found = found || write == *event.Write
			}
			if !found {
				return ErrSourceSession
			}
			_, err = j.CompleteMigrationWrite(id, event.Write.Request, now)
		}
	case "comparison_prepared":
		if event.Write != nil || event.Binding == nil || event.Attempt != "" || event.Count != 0 {
			return ErrSourceSession
		}
		_, err = j.CheckpointMigrationComparison(id, *event.Binding, now)
	case "comparison_completed":
		if event.Write != nil || event.Binding != nil || event.Attempt == "" || event.Count < 1 {
			return ErrSourceSession
		}
		_, err = j.CompleteMigrationComparison(id, event.Attempt, event.Count, now)
	default:
		return ErrSourceSession
	}
	return err
}
