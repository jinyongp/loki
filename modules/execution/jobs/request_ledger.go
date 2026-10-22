package jobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/platform/privateio"
	"loki/internal/platform/safeio"
)

const protectedRequestRetention = 30 * 24 * time.Hour
const protectedLedgerBudget int64 = 64 << 20

var ErrRequestExpired = errors.New("protected Job request has expired")
var ErrDeliveryUnknown = errors.New("protected Job admission result is unavailable; request is spent")
var ErrOutputExpired = errors.New("protected Job output has expired")

// This root-lifetime ledger records admission identity only. Execution state,
// receipts and masked output remain exclusively in the canonical Job record.
type spentRequest struct {
	Version       int    `json:"version"`
	JobID         string `json:"job_id"`
	RequestID     string `json:"request_id"`
	RequestSHA256 string `json:"request_sha256"`
	DeploymentID  string `json:"deployment_id"`
	CreatedAt     string `json:"created_at"`
}

func (s spentRequest) valid() bool {
	id, err := JobIDForRequestID(s.RequestID)
	created, timeErr := time.Parse(time.RFC3339Nano, s.CreatedAt)
	return s.Version == journalVersion && err == nil && id == s.JobID && validReplayIdentity(s.RequestID, s.RequestSHA256) && protectedDeployment.MatchString(s.DeploymentID) && timeErr == nil && created.Location() == time.UTC
}

func sameProtectedSelection(a, b *ProtectedRecord) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.DeploymentID == b.DeploymentID && a.Resource == b.Resource && a.Environment == b.Environment
}

func (j *Journal) reserveSpent(r Record) error {
	s := spentRequest{Version: journalVersion, JobID: r.ID, RequestID: r.RequestID, RequestSHA256: r.RequestSHA256, DeploymentID: r.Protected.DeploymentID, CreatedAt: r.CreatedAt}
	if !s.valid() {
		return errors.New("invalid protected request reservation")
	}
	if previous, ok := j.spent[r.ID]; ok {
		if previous != s {
			return ErrReplayConflict
		}
		return nil
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if j.spentBytes+int64(len(raw)) > protectedLedgerBudget {
		return ErrJournalCapacity
	}
	// A reservation is spent even if the later Job publication or its ACK fails.
	// A retry must never create a fresh container after an uncertain response.
	j.spent[r.ID] = s
	j.spentBytes += int64(len(raw))
	return safeio.PublishPrivate(filepath.Join(j.dir, "requests", r.ID+".json"), raw, false)
}

func (j *Journal) loadSpent() error {
	dir := filepath.Join(j.dir, "requests")
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := validateJournalDirectory(dir); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		f := os.NewFile(uintptr(fd), "protected-request")
		info, statErr := f.Stat()
		raw, readErr := io.ReadAll(io.LimitReader(f, 4097))
		closeErr := f.Close()
		if statErr != nil || readErr != nil || closeErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || len(raw) > 4096 {
			return errors.New("invalid protected request ledger entry")
		}
		if strings.HasPrefix(entry.Name(), ".loki-private-") {
			if err := os.Remove(path); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(entry.Name(), "maintenance-") {
			var m Maintenance
			if privateio.Decode(raw, &m) != nil || !validMaintenance(m) || entry.Name() != maintenanceRequestName(m.OperationID) {
				return errors.New("invalid host maintenance admission history")
			}
			j.spentBytes += maintenanceRequestBudget(raw, m.Active)
			if j.spentBytes > protectedLedgerBudget {
				return ErrJournalCapacity
			}
			j.maintenanceRequests[m.OperationID] = m
			continue
		}
		var s spentRequest
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if err := d.Decode(&s); err != nil || !s.valid() || entry.Name() != s.JobID+".json" {
			return errors.New("invalid protected request ledger entry")
		}
		var trailing any
		if !errors.Is(d.Decode(&trailing), io.EOF) {
			return errors.New("trailing protected request data")
		}
		j.spentBytes += int64(len(raw))
		if j.spentBytes > protectedLedgerBudget {
			return ErrJournalCapacity
		}
		j.spent[s.JobID] = s
	}
	return j.reconcileMaintenanceRequests()
}

// AdmitProtectedRequest reserves the one-attempt identity before any OCI or
// provider effect. All arguments come from the same normalized launch plan.
func (j *Journal) AdmitProtectedRequest(id, backend, request, digest string, network NetworkProfile, endpoints []EndpointRequest, toolchains []ToolchainRef, deployment, resource, environment string, deadline, now time.Time) (Record, bool, error) {
	expected, err := JobIDForRequestID(request)
	if err != nil || expected != id || request != strings.ToLower(request) {
		return Record{}, false, errors.New("invalid protected request identity")
	}
	return j.admit(id, backend, request, digest, network, endpoints, toolchains, deadline, now, true, &ProtectedRecord{DeploymentID: deployment, Resource: resource, Environment: environment})
}
