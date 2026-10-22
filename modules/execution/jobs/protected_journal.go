package jobs

import (
	"errors"
	"regexp"
	"time"

	"loki/internal/platform/privateio"
)

// ProtectedRecord extends the canonical Job record with safe binding metadata
// and the durable masked prefix. It never contains inputs or matcher state.
type ProtectedRecord struct {
	Authority         *privateio.JobAuthority    `json:"authority,omitempty"`
	CancelIntent      *privateio.OperationIntent `json:"cancel_intent,omitempty"`
	CancelResult      *privateio.ReportResult    `json:"cancel_result,omitempty"`
	CancelRequestedAt string                     `json:"cancel_requested_at,omitempty"`
	OutputExpiresAt   string                     `json:"output_expires_at,omitempty"`
	OutputExpired     bool                       `json:"output_expired,omitempty"`
	Cursor            int64                      `json:"cursor"`
	Gap               *OutputGap                 `json:"gap,omitempty"`
	DeploymentID      string                     `json:"deployment_id"`
	Reports           []ProtectedReport          `json:"reports,omitempty"`
	Resource          string                     `json:"resource"`
	Environment       string                     `json:"environment"`
	Prepared          *privateio.PreparedJob     `json:"prepared,omitempty"`
	Output            []byte                     `json:"output,omitempty"`
	Truncated         bool                       `json:"truncated"`
	Complete          bool                       `json:"complete"`
}

type OutputGap struct {
	Cursor        int64  `json:"cursor"`
	Reason        string `json:"reason"`
	UnknownLength bool   `json:"unknown_length"`
}

func (g OutputGap) valid() bool {
	if !g.UnknownLength || g.Cursor < 0 || g.Cursor > MaxOutputBytes {
		return false
	}
	switch g.Reason {
	case "collector_lost", "attach_lost", "masking_failed", "storage_failed", "recovery_interrupted":
		return true
	}
	return false
}

func (j *Journal) RequestProtectedCancel(id string, now time.Time) error {
	_, err := j.updateProtected(id, now, func(r *Record) error {
		if r.Protected == nil {
			return errors.New("Job is not protected")
		}
		if r.Protected.CancelRequestedAt == "" {
			r.Protected.CancelRequestedAt = now.UTC().Format(time.RFC3339Nano)
		}
		return nil
	})
	return err
}

func (j *Journal) MarkOutputLost(id, reason string, now time.Time) error {
	_, err := j.updateProtected(id, now, func(r *Record) error {
		if r.Protected == nil {
			return errors.New("Job is not protected")
		}
		if r.Protected.Complete {
			return nil
		}
		r.Protected.Gap = &OutputGap{Cursor: r.Protected.Cursor, Reason: reason, UnknownLength: true}
		return nil
	})
	return err
}

var protectedDeployment = regexp.MustCompile(`^deployment-[0-9a-f]{32}$`)

func (p *ProtectedRecord) validReports(instance, jobID string) bool {
	if !protectedDeployment.MatchString(p.DeploymentID) || len(p.Reports) > 2 {
		return false
	}
	for i, report := range p.Reports {
		fact := report.Intent.Fact
		if report.Intent.Revision == 0 || report.Intent.RequestID != privateio.OperationID(p.DeploymentID, jobID, "report-"+fact.State) || fact.ExternalRef != privateio.ExternalReference(instance) {
			return false
		}
		if i == 0 {
			if fact.State != "started" || fact.ExitCode != nil || fact.Reason != "" {
				return false
			}
		} else if fact.State == "finished" {
			if fact.ExitCode == nil || *fact.ExitCode < 0 || *fact.ExitCode > 255 || fact.Reason != "" {
				return false
			}
		} else if fact.State != "unknown" || fact.ExitCode != nil || fact.Reason != "outcome_unavailable" {
			return false
		}
		if report.Result != nil && report.Result.Recorded && report.Result.Revision < report.Intent.Revision {
			return false
		}
	}
	return true
}

type ProtectedReport struct {
	Intent privateio.ReportIntent  `json:"intent"`
	Result *privateio.ReportResult `json:"result,omitempty"`
}

func (j *Journal) RecordEngineReport(id string, intent privateio.ReportIntent, result *privateio.ReportResult, now time.Time) error {
	_, err := j.updateProtectedRecord(id, now, true, func(r *Record) error {
		if r.Protected == nil || r.Protected.Prepared == nil {
			return errors.New("protected engine report is not bound")
		}
		for i := range r.Protected.Reports {
			previous := &r.Protected.Reports[i]
			if previous.Intent.RequestID == intent.RequestID {
				if !sameReportIntent(previous.Intent, intent) {
					return errors.New("protected report intent changed")
				}
				if result == nil {
					return nil
				}
				if previous.Result != nil && previous.Result.Recorded {
					return errors.New("protected engine report already attempted")
				}
				copy := *result
				previous.Result = &copy
				return nil
			}
		}
		if len(r.Protected.Reports) >= 2 || result != nil || intent.Revision == 0 {
			return errors.New("invalid protected engine report")
		}
		r.Protected.Reports = append(r.Protected.Reports, ProtectedReport{Intent: intent})
		return nil
	})
	return err
}

func (j *Journal) updateProtected(id string, now time.Time, update func(*Record) error) (Record, error) {
	return j.updateProtectedRecord(id, now, false, update)
}

func (j *Journal) updateProtectedRecord(id string, now time.Time, terminal bool, update func(*Record) error) (Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.ready(); err != nil {
		return Record{}, err
	}
	current, ok := j.records[id]
	if !ok || current.State == StateTerminal && !terminal {
		return Record{}, errors.New("protected Job is not active")
	}
	record := cloneRecord(current)
	if err := update(&record); err != nil {
		return Record{}, err
	}
	record.UpdatedAt = now.UTC().Format(time.RFC3339Nano)
	if !record.valid(j.limits.MaxOutputBytes) {
		return Record{}, errors.New("protected Job binding is invalid")
	}
	if err := j.publish(record, true); err != nil {
		return Record{}, err
	}
	j.records[id] = cloneRecord(record)
	return cloneRecord(record), nil
}

func sameReportIntent(a, b privateio.ReportIntent) bool {
	return a.RequestID == b.RequestID && a.Revision == b.Revision && a.Fact.State == b.Fact.State && a.Fact.ExternalRef == b.Fact.ExternalRef && a.Fact.Reason == b.Fact.Reason && (a.Fact.ExitCode == nil) == (b.Fact.ExitCode == nil) && (a.Fact.ExitCode == nil || *a.Fact.ExitCode == *b.Fact.ExitCode)
}

func (j *Journal) BindProtectedAuthority(id string, authority privateio.JobAuthority, now time.Time) error {
	_, err := j.updateProtected(id, now, func(r *Record) error {
		if r.Protected == nil || r.Protected.Authority != nil {
			return errors.New("protected authority already bound")
		}
		r.Protected.Authority = &authority
		return nil
	})
	return err
}

func (j *Journal) RecordInputCancel(id string, intent privateio.OperationIntent, result *privateio.ReportResult, now time.Time) error {
	_, err := j.updateProtectedRecord(id, now, true, func(r *Record) error {
		p := r.Protected
		if p == nil || intent.Revision == 0 || intent.RequestID != privateio.OperationID(p.DeploymentID, id, "cancel") {
			return errors.New("invalid input cancellation")
		}
		if p.CancelIntent != nil && *p.CancelIntent != intent {
			return errors.New("input cancellation changed")
		}
		p.CancelIntent = &intent
		if result != nil {
			copy := *result
			p.CancelResult = &copy
		}
		return nil
	})
	return err
}

func (j *Journal) BindProtectedJob(id, deployment, resource, environment string, now time.Time) (Record, error) {
	return j.updateProtected(id, now, func(r *Record) error {
		if r.State != StateAdmitted || r.RequestID == "" || r.Protected != nil {
			return errors.New("protected Job selection cannot change")
		}
		r.Protected = &ProtectedRecord{DeploymentID: deployment, Resource: resource, Environment: environment}
		return j.reserveSpent(*r)
	})
}

func (j *Journal) BindProtectedInput(id string, prepared privateio.PreparedJob, now time.Time) (Record, error) {
	return j.updateProtected(id, now, func(r *Record) error {
		if r.State != StateAdmitted || r.InstanceRef == "" || r.Protected == nil {
			return errors.New("protected Job input cannot change")
		}
		if previous := r.Protected.Prepared; previous != nil {
			if previous.Stage != "prepared" || prepared.Stage != "approved" || previous.ExecutionID != prepared.ExecutionID || previous.InputID != prepared.InputID || previous.RunnerID != prepared.RunnerID || previous.RunnerInstanceID != prepared.RunnerInstanceID || previous.PrepareRequestID != prepared.PrepareRequestID || previous.ApproveRequestID != prepared.ApproveRequestID || previous.DeliverRequestID != prepared.DeliverRequestID || prepared.Revision <= previous.Revision {
				return errors.New("protected input binding cannot change")
			}
		} else if prepared.Stage != "prepared" {
			return errors.New("protected input must be prepared first")
		}
		r.Protected.Prepared = &prepared
		return nil
	})
}

func (j *Journal) AppendMaskedOutput(id string, body []byte, now time.Time) error {
	_, err := j.updateProtected(id, now, func(r *Record) error {
		if r.Protected == nil || r.Protected.Prepared == nil || r.Protected.Complete || r.Protected.Gap != nil {
			return errors.New("protected output is not writable")
		}
		remaining := j.limits.MaxOutputBytes - len(r.Protected.Output)
		n := min(len(body), remaining)
		r.Protected.Output = append(r.Protected.Output, body[:n]...)
		r.Protected.Cursor = int64(len(r.Protected.Output))
		r.Protected.Truncated = r.Protected.Truncated || n < len(body)
		return nil
	})
	return err
}

func (j *Journal) MarkFilteredEOF(id string, now time.Time) error {
	_, err := j.updateProtected(id, now, func(r *Record) error {
		if r.Protected == nil || r.Protected.Prepared == nil || r.Protected.Gap != nil {
			return errors.New("protected output is not bound")
		}
		r.Protected.Complete = true
		return nil
	})
	return err
}
