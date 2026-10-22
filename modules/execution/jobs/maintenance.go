package jobs

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
	"loki/internal/platform/privateio"
	"loki/internal/platform/safeio"
)

var ErrMaintenanceBusy = errors.New("Job admission is fenced by host maintenance")
var ErrJobsBusy = errors.New("host maintenance requires all Jobs to finish or explicit interruption")

// Maintenance is host-operation intent in the existing Job journal. Job
// execution and cleanup remain exclusively in their canonical records.
type Maintenance struct {
	Version     int    `json:"version"`
	OperationID string `json:"operation_id"`
	Interrupt   bool   `json:"interrupt"`
	Active      bool   `json:"active"`
}

const maintenanceFile = "maintenance.json"

func loadMaintenance(dir string) (*Maintenance, error) {
	fd, err := unix.Open(filepath.Join(dir, maintenanceFile), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "job-maintenance")
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Nlink != 1 || st.Size > 4096 || st.Uid != 0 && st.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("invalid Job maintenance metadata")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return nil, err
	}
	var value Maintenance
	err = privateio.Decode(raw, &value)
	if err != nil || value.Version != 1 || !requestIDPattern.MatchString(value.OperationID) || value.OperationID != strings.ToLower(value.OperationID) {
		return nil, errors.New("invalid Job maintenance metadata")
	}
	return &value, nil
}

func unresolved(record Record) bool {
	return record.State != StateTerminal || record.Result == nil || record.Result.Cleanup != CleanupComplete && record.Result.Cleanup != CleanupNotRequired
}

func (j *Journal) publishMaintenance(value Maintenance) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return safeio.PublishPrivate(filepath.Join(j.dir, maintenanceFile), raw, true)
}

// BeginMaintenance atomically checks retained Job cleanup and closes admission.
// The launcher additionally waits for in-flight pre-admission work before calling
// this method. A lost response is retried with the original host operation UUID.
func (j *Journal) BeginMaintenance(operationID string, interrupt bool) (Maintenance, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.ready(); err != nil {
		return Maintenance{}, err
	}
	if !requestIDPattern.MatchString(operationID) || operationID != strings.ToLower(operationID) {
		return Maintenance{}, errors.New("invalid host operation identity")
	}
	if previous := j.maintenance; previous != nil {
		if previous.OperationID == operationID {
			if previous.Interrupt != interrupt {
				return Maintenance{}, ErrReplayConflict
			}
			if previous.Active {
				// Retry publication as well after uncertain storage failure.
				return *previous, j.publishMaintenance(*previous)
			}
			return *previous, nil
		}
		if previous.Active {
			return Maintenance{}, ErrMaintenanceBusy
		}
	}
	if !interrupt {
		for _, record := range j.records {
			if unresolved(record) {
				return Maintenance{}, ErrJobsBusy
			}
		}
	}
	value := Maintenance{Version: 1, OperationID: operationID, Interrupt: interrupt, Active: true}
	// An uncertain publication always leaves this process fenced.
	j.maintenance = &value
	return value, j.publishMaintenance(value)
}

func (j *Journal) EndMaintenance(operationID string) (Maintenance, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.ready(); err != nil {
		return Maintenance{}, err
	}
	if j.maintenance == nil || j.maintenance.OperationID != operationID {
		return Maintenance{}, ErrMaintenanceBusy
	}
	for _, record := range j.records {
		if unresolved(record) {
			return Maintenance{}, ErrJobsBusy
		}
	}
	value := *j.maintenance
	value.Active = false
	if err := j.publishMaintenance(value); err != nil {
		return Maintenance{}, err
	}
	j.maintenance = &value
	return value, nil
}

func (j *Journal) AdmissionAllowed() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.ready(); err != nil {
		return err
	}
	if j.maintenance != nil && j.maintenance.Active {
		return ErrMaintenanceBusy
	}
	return nil
}
