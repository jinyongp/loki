package launcher

import (
	"context"

	"loki/modules/execution/jobs"
)

type maintenanceRequest struct {
	Operation   string `json:"operation"`
	OperationID string `json:"operation_id"`
	Interrupt   bool   `json:"interrupt"`
}

// RWMutex reserves the writer before waiting for pre-admission resolution.
// Cancellation hands eventual ownership back without leaving a stuck writer.
// RPC's bounded connection pool bounds these waiters as well.
func (l *lifecycle) lockAdmission(ctx context.Context) error {
	acquired := make(chan struct{})
	abandoned := make(chan struct{})
	go func() {
		l.admission.Lock()
		select {
		case acquired <- struct{}{}:
		case <-abandoned:
			l.admission.Unlock()
		}
	}()
	select {
	case <-acquired:
		return nil
	case <-ctx.Done():
		close(abandoned)
		return ctx.Err()
	}
}

func (l *lifecycle) beginMaintenance(ctx context.Context, request maintenanceRequest) (jobs.Maintenance, error) {
	if err := l.lockAdmission(ctx); err != nil {
		return jobs.Maintenance{}, err
	}
	defer l.admission.Unlock()
	previous, err := l.journal.MaintenanceStatus()
	if err != nil {
		return jobs.Maintenance{}, err
	}
	if previous != nil && previous.OperationID == request.OperationID && !previous.Active {
		return l.journal.BeginMaintenance(request.OperationID, request.Interrupt)
	}
	if !request.Interrupt {
		l.mu.Lock()
		busy := len(l.active) != 0
		l.mu.Unlock()
		if busy {
			return jobs.Maintenance{}, jobs.ErrJobsBusy
		}
	}
	state, err := l.journal.BeginMaintenance(request.OperationID, request.Interrupt)
	if err != nil || !state.Active || !request.Interrupt {
		return state, err
	}
	for {
		// Persist each protected cancellation intent before signaling its owner.
		l.mu.Lock()
		active := make(map[string]*ownedJob, len(l.active))
		for id, job := range l.active {
			active[id] = job
		}
		l.mu.Unlock()
		for id, job := range active {
			if _, err := l.cancelStatus(ctx, id); err != nil {
				return state, err
			}
			select {
			case <-job.done:
			case <-ctx.Done():
				return state, ctx.Err()
			}
		}
		// With admission blocked and live owners stopped, the existing recovery
		// path reconciles original OCI identities and terminal cleanup failures.
		if err := ctx.Err(); err != nil {
			return state, err
		}
		if err := l.reconcile(); err != nil {
			return state, err
		}
		records, err := l.journal.List()
		if err != nil {
			return state, err
		}
		pending := false
		for _, record := range records {
			if record.State != jobs.StateTerminal {
				pending = true
			} else if record.Result == nil || record.Result.Cleanup != jobs.CleanupComplete && record.Result.Cleanup != jobs.CleanupNotRequired {
				return state, jobs.ErrJobsBusy
			}
		}
		if !pending {
			return state, nil
		}
		l.mu.Lock()
		recovered := len(l.active)
		l.mu.Unlock()
		if recovered == 0 {
			return state, jobs.ErrJobsBusy
		}
	}
}
