//go:build linux

package privateio

import (
	"context"
	"net"
	"time"
)

// ReconcileJob can report facts or cancel a pending input. There is deliberately
// no prepare/approve/deliver operation on this recovery connection.
func ReconcileJob(ctx context.Context, socket string, uid uint32, request ReconcileJobRequest, persist func(OperationIntent) error) (ReportResult, error) {
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return ReportResult{}, ErrInvalid
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	u, ok := c.(*net.UnixConn)
	if !ok || VerifyPeer(u, uid) != nil || persist == nil || WriteControl(c, JobRequest{Reconcile: &request}) != nil {
		return ReportResult{}, ErrInvalid
	}
	var intent OperationIntent
	name := request.Action
	if request.Action == "report" && request.Fact != nil {
		name = "report-" + request.Fact.State
	}
	if ReadControl(c, &intent) != nil || intent.Revision == 0 || intent.RequestID != OperationID(request.Authority.DeploymentID, request.JobID, name) || request.Intent != nil && *request.Intent != intent {
		return ReportResult{}, ErrInvalid
	}
	if persist(intent) != nil || WriteControl(c, intent) != nil {
		return ReportResult{}, ErrInvalid
	}
	var result ReportResult
	if ReadControl(c, &result) != nil || result.Recorded && result.Revision < intent.Revision {
		return ReportResult{}, ErrInvalid
	}
	return result, nil
}
