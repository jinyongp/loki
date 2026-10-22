//go:build linux

package secretbroker

import (
	"context"
	"net"
	"time"

	controlpolicy "loki/internal/control/policy"
	"loki/internal/platform/privateio"
	provider "loki/modules/secrets/devtools"
)

func (s *Service) reconcileJob(ctx context.Context, conn *net.UnixConn, request privateio.ReconcileJobRequest) {
	a := request.Authority
	if a.DeploymentID != s.options.DeploymentID || a.PolicySHA256 != s.options.PolicySHA256 || !idPattern.MatchString(request.JobID) {
		return
	}
	var resource *controlpolicy.ApplicationResource
	for i := range s.options.Resources {
		r := &s.options.Resources[i]
		if r.ID == a.ResourceID && r.Alias == a.Resource && r.Authorize(a.Principal, a.Repository, a.Environment, "execute") == nil {
			resource = r
			break
		}
	}
	if resource == nil || request.Prepared.ExecutionID == "" || request.Prepared.InputID == "" {
		return
	}
	if request.Action == "report" {
		fact := request.Fact
		if fact == nil || fact.ExternalRef != privateio.ExternalReference(request.InstanceRef) {
			return
		}
		switch fact.State {
		case "started":
			if fact.ExitCode != nil || fact.Reason != "" {
				return
			}
		case "finished":
			if fact.ExitCode == nil || *fact.ExitCode < 0 || *fact.ExitCode > 255 || fact.Reason != "" {
				return
			}
		case "unknown":
			if fact.ExitCode != nil || fact.Reason != "outcome_unavailable" {
				return
			}
		default:
			return
		}
	} else if request.Action != "cancel" || request.Fact != nil {
		return
	}
	ctx, stop := context.WithTimeout(ctx, 4*time.Second)
	defer stop()
	current, err := s.options.Provider.ExecutionStatus(ctx, resource.Profile(), request.Prepared.ExecutionID)
	if err != nil || current.InputID != request.Prepared.InputID || current.JobID != request.JobID || current.RunnerID != "" && current.RunnerID != request.Prepared.RunnerID || current.InstanceID != "" && current.InstanceID != request.Prepared.RunnerInstanceID {
		return
	}
	if current.ExternalRef != "" && current.ExternalRef != privateio.ExternalReference(request.InstanceRef) {
		return
	}
	if request.Action == "cancel" && current.DeliveryState != "prepared" && current.DeliveryState != "approved" && current.DeliveryState != "canceled" && current.DeliveryState != "expired" {
		return
	}
	name := request.Action
	if request.Fact != nil {
		name = "report-" + request.Fact.State
	}
	intent := privateio.OperationIntent{RequestID: privateio.OperationID(a.DeploymentID, request.JobID, name), Revision: current.Revision}
	if request.Intent != nil {
		if request.Intent.RequestID != intent.RequestID || request.Intent.Revision == 0 {
			return
		}
		intent = *request.Intent
	}
	if privateio.WriteControl(conn, intent) != nil {
		return
	}
	var ack privateio.OperationIntent
	if privateio.ReadControl(conn, &ack) != nil || ack != intent {
		return
	}
	current.Revision = intent.Revision
	if request.Action == "cancel" {
		current, err = s.options.Provider.Cancel(ctx, resource.Profile(), intent.RequestID, current)
	} else {
		f := request.Fact
		current, err = s.options.Provider.Report(ctx, resource.Profile(), intent.RequestID, request.Prepared.RunnerID, current, provider.EngineFact{State: f.State, ExternalRef: f.ExternalRef, ExitCode: f.ExitCode, Reason: f.Reason})
	}
	result := privateio.ReportResult{}
	if err == nil {
		result.Recorded = true
		result.Revision = current.Revision
	}
	_ = privateio.WriteControl(conn, result)
}
