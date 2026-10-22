//go:build linux

package launcher

import (
	"context"
	"loki/internal/platform/privateio"
	"loki/modules/execution/jobs"
	"time"
)

func (l *lifecycle) protectedAuthority(open privateio.JobOpen) privateio.JobAuthority {
	a := privateio.JobAuthority{DeploymentID: open.DeploymentID, PolicySHA256: open.PolicySHA256, Principal: open.Principal, Repository: open.Repository, Resource: open.Resource, Environment: open.Environment}
	for _, resource := range l.protected.Resources {
		if resource.Alias == open.Resource {
			a.ResourceID = resource.ID
			break
		}
	}
	return a
}

// Recovery uses only the original identity and observed facts. Failure to reach
// a locked/unavailable provider never resumes a workload or delivers again.
func (l *lifecycle) reconcileProtectedReports(ctx context.Context, r jobs.Record) error {
	p := r.Protected
	if r.ExpiresAt != "" {
		if expires, err := time.Parse(time.RFC3339Nano, r.ExpiresAt); err == nil && !l.now().Before(expires) {
			return nil
		}
	}
	if l.protected == nil || p == nil || p.Prepared == nil || p.Authority == nil {
		return nil
	}
	request := privateio.ReconcileJobRequest{Authority: *p.Authority, JobID: r.ID, InstanceRef: r.InstanceRef, Prepared: *p.Prepared}
	var persistenceErr error
	if len(p.Reports) == 0 && (p.CancelResult == nil || !p.CancelResult.Recorded) {
		request.Action = "cancel"
		request.Intent = p.CancelIntent
		result, err := privateio.ReconcileJob(ctx, l.protected.Socket, l.protected.BrokerUID, request, func(intent privateio.OperationIntent) error {
			persistenceErr = l.journal.RecordInputCancel(r.ID, intent, nil, l.now())
			return persistenceErr
		})
		if persistenceErr != nil {
			return persistenceErr
		}
		if err == nil {
			latest, _, _ := l.journal.Get(r.ID)
			if latest.Protected.CancelIntent != nil {
				return l.journal.RecordInputCancel(r.ID, *latest.Protected.CancelIntent, &result, l.now())
			}
		}
		return nil
	}
	reports := append([]jobs.ProtectedReport(nil), p.Reports...)
	if len(reports) == 1 && r.Result != nil {
		fact := privateio.JobFact{State: "unknown", Reason: "outcome_unavailable", ExternalRef: privateio.ExternalReference(r.InstanceRef)}
		if r.Result.ExitCode != nil {
			fact.State = "finished"
			fact.Reason = ""
			value := *r.Result.ExitCode
			fact.ExitCode = &value
		}
		intent := privateio.ReportIntent{Fact: fact}
		if previous := reports[0].Result; previous != nil && previous.Recorded {
			intent.RequestID = privateio.OperationID(p.DeploymentID, r.ID, "report-"+fact.State)
			intent.Revision = previous.Revision
			if err := l.journal.RecordEngineReport(r.ID, intent, nil, l.now()); err != nil {
				return err
			}
		}
		reports = append(reports, jobs.ProtectedReport{Intent: intent})
	}
	for _, report := range reports {
		if report.Result != nil && report.Result.Recorded {
			continue
		}
		request.Action = "report"
		request.Fact = &report.Intent.Fact
		request.Intent = nil
		if report.Intent.RequestID != "" {
			request.Intent = &privateio.OperationIntent{RequestID: report.Intent.RequestID, Revision: report.Intent.Revision}
		}
		var committed privateio.ReportIntent
		result, err := privateio.ReconcileJob(ctx, l.protected.Socket, l.protected.BrokerUID, request, func(intent privateio.OperationIntent) error {
			committed = privateio.ReportIntent{RequestID: intent.RequestID, Revision: intent.Revision, Fact: *request.Fact}
			persistenceErr = l.journal.RecordEngineReport(r.ID, committed, nil, l.now())
			return persistenceErr
		})
		if persistenceErr != nil {
			return persistenceErr
		}
		if err != nil {
			return nil
		}
		if err := l.journal.RecordEngineReport(r.ID, committed, &result, l.now()); err != nil {
			return err
		}
		if !result.Recorded {
			return nil
		}
	}
	return nil
}
