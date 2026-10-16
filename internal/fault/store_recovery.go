package fault

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const StoreRecoveryPrefix = "LOKI_STORE_RECOVERY "

// StoreRecovery is a deliberately small public diagnostic. Only known owners
// and reasons cross process boundaries; arbitrary service logs never do.
type StoreRecovery struct {
	Schema int    `json:"schema"`
	Owner  string `json:"owner"`
	Reason string `json:"reason"`
}

func (r StoreRecovery) Valid() bool {
	if r.Schema != 1 || (r.Owner != "secrets" && r.Owner != "github") {
		return false
	}
	switch r.Reason {
	case "missing_key", "missing_data", "invalid_record", "conflicting_record", "initialization_pending", "unreadable_store":
		return true
	}
	return false
}

func (r *StoreRecovery) Error() string {
	if r == nil || !r.Valid() {
		return "Encrypted store diagnostics are unavailable; run loki doctor"
	}
	owner := map[string]string{"secrets": "Application secrets", "github": "GitHub credentials"}[r.Owner]
	if r.Reason == "initialization_pending" {
		return owner + " initialization was interrupted. Loki retained its recovery record. Run 'loki tools restart' to resume the saved initialization; restarting services may interrupt active jobs."
	}
	reason := "its contents cannot be decrypted"
	switch r.Reason {
	case "missing_key":
		reason = "its encryption key is missing"
	case "missing_data":
		reason = "its encrypted data is missing"
	case "invalid_record":
		reason = "its initialization recovery record is damaged"
	case "conflicting_record":
		reason = "its saved initialization conflicts with existing data"
	}
	return owner + " cannot start because " + reason + ". Automatic recovery is unavailable; Loki cannot prove this is an unused store and has not reset it. Run 'loki backups list' and restore a known working backup with 'loki restore BACKUP-ID'. Restore also reverts programs, settings and tool data. Without a working backup, existing secrets cannot be recovered from this state alone."
}

func (r StoreRecovery) NextActions() []string {
	if !r.Valid() {
		return nil
	}
	if r.Reason == "initialization_pending" {
		return []string{"loki tools restart — restart services and resume recorded initialization; active jobs may be interrupted"}
	}
	return []string{"loki backups list — find a known working backup on this host", "loki restore BACKUP-ID — restore programs, settings and data from that backup"}
}

func WriteStoreRecovery(w io.Writer, err error) {
	var recovery *StoreRecovery
	if !errors.As(err, &recovery) || !recovery.Valid() {
		return
	}
	encoded, _ := json.Marshal(recovery)
	fmt.Fprintf(w, "%s%s\n", StoreRecoveryPrefix, encoded)
}

func ParseStoreRecovery(data []byte) *StoreRecovery {
	lines := bytes.Split(data, []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		line := string(lines[i])
		if !strings.HasPrefix(line, StoreRecoveryPrefix) || len(line) > 1024 {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(strings.TrimPrefix(line, StoreRecoveryPrefix)))
		decoder.DisallowUnknownFields()
		var recovery StoreRecovery
		var trailing any
		if decoder.Decode(&recovery) == nil && decoder.Decode(&trailing) == io.EOF && recovery.Valid() {
			return &recovery
		}
	}
	return nil
}
