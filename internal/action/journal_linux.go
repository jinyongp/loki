package action

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"

	"loki/internal/fault"
)

const maxMaterializationJournal = 65536

type materializationRecord struct {
	Version                       int    `json:"version"`
	Target                        string `json:"target"`
	Device, Inode                 uint64
	Quarantine                    string `json:"quarantine,omitempty"`
	Phase                         string `json:"phase,omitempty"`
	Explicit                      bool   `json:"explicit,omitempty"`
	ParentDevice, ParentInode     uint64
	RecoveryDevice, RecoveryInode uint64
}

func (r *materializationRecord) valid() bool {
	if r == nil {
		return true
	}
	if (r.Version != 1 && r.Version != 2) || !relativeFile(r.Target) || r.Inode == 0 {
		return false
	}
	if r.Quarantine == "" {
		return r.Phase == "" && !r.Explicit
	}
	return r.Version == 2 && launchTokenPattern.MatchString(r.Quarantine) &&
		(r.Phase == "capture" || r.Phase == "captured" || r.Phase == "restore") &&
		r.ParentInode != 0 && r.RecoveryInode != 0
}

type materializationFrame struct {
	Record json.RawMessage `json:"record"`
	SHA256 string          `json:"sha256"`
}

func materializationJournal(marker *os.File) (*materializationRecord, []byte, int, error) {
	if marker == nil {
		return nil, nil, 0, errors.New("missing materialization marker")
	}
	if _, err := marker.Seek(0, io.SeekStart); err != nil {
		return nil, nil, 0, err
	}
	data, err := io.ReadAll(io.LimitReader(marker, maxMaterializationJournal+1))
	if err != nil {
		return nil, nil, 0, err
	}
	invalid := fault.Error("materialized secret marker is invalid")
	if len(data) > maxMaterializationJournal {
		return nil, nil, 0, invalid
	}
	var record *materializationRecord
	validEnd := 0
	for offset := 0; offset < len(data); {
		end := bytes.IndexByte(data[offset:], '\n')
		terminated := end >= 0
		if terminated {
			end += offset
		} else {
			end = len(data)
		}
		line := data[offset:end]
		var next *materializationRecord
		// Read the original one-record journal without rewriting its inode:
		// the same descriptor also owns the cross-process flock.
		if offset == 0 && json.Unmarshal(line, &next) == nil && next != nil && next.Version == 1 && next.valid() {
			record = next
		} else {
			var frame materializationFrame
			if !terminated || json.Unmarshal(line, &frame) != nil || len(frame.Record) == 0 {
				break
			}
			sum := sha256.Sum256(frame.Record)
			if hex.EncodeToString(sum[:]) != frame.SHA256 || json.Unmarshal(frame.Record, &next) != nil || !next.valid() {
				break
			}
			record = next
		}
		validEnd = end
		if terminated {
			validEnd++
		}
		offset = validEnd
	}
	if len(data) != 0 && validEnd == 0 {
		return nil, nil, 0, invalid
	}
	return record, data, validEnd, nil
}

func readMaterializationRecord(marker *os.File) (*materializationRecord, error) {
	record, _, _, err := materializationJournal(marker)
	return record, err
}

// Append a checksummed, newline-committed frame before filesystem mutations.
// A torn trailing frame leaves the preceding synced intent usable for recovery.
// Tombstones are synced before truncating an idle journal, keeping growth bounded.
func writeMaterializationRecord(marker *os.File, record *materializationRecord) error {
	if !record.valid() {
		return fault.Error("materialized secret marker is invalid")
	}
	_, data, validEnd, err := materializationJournal(marker)
	if err != nil {
		return err
	}
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	frame, err := json.Marshal(materializationFrame{Record: body, SHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		return err
	}
	if validEnd > 0 && data[validEnd-1] != '\n' {
		frame = append([]byte{'\n'}, frame...)
	}
	frame = append(frame, '\n')
	if validEnd+len(frame) > maxMaterializationJournal {
		return fault.Error("materialized secret journal requires recovery")
	}
	if len(data) != validEnd {
		if err := marker.Truncate(int64(validEnd)); err != nil {
			return err
		}
	}
	if _, err := marker.WriteAt(frame, int64(validEnd)); err != nil {
		return err
	}
	if err := marker.Sync(); err != nil {
		return err
	}
	if record == nil {
		if err := marker.Truncate(0); err != nil {
			return err
		}
		return marker.Sync()
	}
	return nil
}
