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

func validMaintenance(m Maintenance) bool {
	return m.Version == 1 && requestIDPattern.MatchString(m.OperationID) && m.OperationID == strings.ToLower(m.OperationID)
}

func maintenanceRequestName(id string) string {
	return "maintenance-" + strings.ReplaceAll(id, "-", "") + ".json"
}

func maintenanceRequestBudget(raw []byte, active bool) int64 {
	n := int64(len(raw))
	if active {
		n++
	} // Reserve the extra byte in the terminal JSON false value.
	return n
}

// Host admission identities share the existing 64MiB root-lifetime request
// budget. They are domain-separated from Job IDs and carry no execution state.
// Reserve before closing admission/signaling; completion can never need more
// capacity than the admitted intent already reserved.
func (j *Journal) reserveMaintenanceRequest(m Maintenance) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	previous, exists := j.maintenanceRequests[m.OperationID]
	if exists && (previous.Interrupt != m.Interrupt || !previous.Active && m.Active) {
		return ErrReplayConflict
	}
	if !exists && j.spentBytes+maintenanceRequestBudget(raw, m.Active) > protectedLedgerBudget {
		return ErrJournalCapacity
	}
	if err := safeio.PublishPrivate(filepath.Join(j.dir, "requests", maintenanceRequestName(m.OperationID)), raw, true); err != nil {
		return err
	}
	if !exists {
		j.spentBytes += maintenanceRequestBudget(raw, m.Active)
	}
	j.maintenanceRequests[m.OperationID] = m
	return nil
}

func (j *Journal) reconcileMaintenanceRequests() error {
	var pending *Maintenance
	for _, m := range j.maintenanceRequests {
		if m.Active {
			if pending != nil {
				return errors.New("multiple unfinished host admission identities")
			}
			copy := m
			pending = &copy
		}
	}
	if current := j.maintenance; current != nil {
		proof, exists := j.maintenanceRequests[current.OperationID]
		if !exists || proof.Interrupt != current.Interrupt {
			return errors.New("host admission history is unsupported or incomplete; paired recovery is required")
		}
		if current.Active && !proof.Active {
			copy := proof
			j.maintenance = &copy // Completion was durable before a lost release ACK.
		}
	}
	if pending != nil {
		if j.maintenance != nil && j.maintenance.Active && j.maintenance.OperationID != pending.OperationID {
			return errors.New("host admission identity conflicts with its durable fence")
		}
		j.maintenance = pending // Recover a crash after reservation/before fence publication.
	}
	return nil
}

// MaintenanceReplay answers an old completed operation without changing the
// current fence or consulting Jobs admitted after that operation completed.
func (j *Journal) MaintenanceReplay(id string, interrupt bool) (Maintenance, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.ready(); err != nil {
		return Maintenance{}, false, err
	}
	m, exists := j.maintenanceRequests[id]
	if !exists || m.Active {
		return Maintenance{}, false, nil
	}
	if m.Interrupt != interrupt {
		return Maintenance{}, true, ErrReplayConflict
	}
	return m, true, nil
}

func (j *Journal) MaintenanceStatus() (*Maintenance, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.ready(); err != nil {
		return nil, err
	}
	if j.maintenance == nil {
		return nil, nil
	}
	value := *j.maintenance
	return &value, nil
}

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
	if err != nil || !validMaintenance(value) {
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
	if completed, exists := j.maintenanceRequests[operationID]; exists && !completed.Active {
		if completed.Interrupt != interrupt {
			return Maintenance{}, ErrReplayConflict
		}
		return completed, nil
	}
	if previous := j.maintenance; previous != nil {
		if previous.OperationID == operationID {
			if previous.Interrupt != interrupt {
				return Maintenance{}, ErrReplayConflict
			}
			if previous.Active {
				if err := j.reserveMaintenanceRequest(*previous); err != nil {
					return *previous, err
				}
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
	raw, _ := json.Marshal(value)
	if _, exists := j.maintenanceRequests[operationID]; !exists && j.spentBytes+maintenanceRequestBudget(raw, true) > protectedLedgerBudget {
		return Maintenance{}, ErrJournalCapacity
	}
	// An uncertain publication always leaves this process fenced.
	j.maintenance = &value
	if err := j.reserveMaintenanceRequest(value); err != nil {
		return value, err
	}
	return value, j.publishMaintenance(value)
}

func (j *Journal) EndMaintenance(operationID string) (Maintenance, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.ready(); err != nil {
		return Maintenance{}, err
	}
	if completed, exists := j.maintenanceRequests[operationID]; exists && !completed.Active {
		if current := j.maintenance; current != nil && current.OperationID == operationID && current.Active {
			if err := j.publishMaintenance(completed); err != nil {
				return Maintenance{}, err
			}
			copy := completed
			j.maintenance = &copy
		}
		return completed, nil
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
	if err := j.reserveMaintenanceRequest(value); err != nil {
		return Maintenance{}, err
	}
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
