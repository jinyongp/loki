//go:build linux

package launcher

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	controlpolicy "loki/internal/control/policy"
	"loki/internal/platform/privateio"
	"loki/internal/platform/sandbox"
	"loki/modules/execution/jobs"
)

// ProtectedOptions is trusted host composition. The resolver observes the
// actual repository owning CWD; no API request supplies its identity.
type ProtectedOptions struct {
	Socket       string
	BrokerUID    uint32
	Principal    string
	DeploymentID string
	Resources    []controlpolicy.ApplicationResource
	Repository   func(context.Context, string) (string, error)
}

type protectedRunner interface {
	PrepareProtectedJob(context.Context, sandbox.Plan) (sandbox.StartResult, error)
	AttachProtectedJob(context.Context, sandbox.Resource, string) (*sandbox.ProtectedAttachment, error)
}

func (l *lifecycle) configureProtected(options *ProtectedOptions) error {
	if options == nil {
		return nil
	}
	if !filepath.IsAbs(options.Socket) || filepath.Clean(options.Socket) != options.Socket || options.BrokerUID == 0 || options.Principal == "" || options.DeploymentID == "" || options.Repository == nil {
		return errors.New("invalid protected Job configuration")
	}
	if _, ok := l.runner.(protectedRunner); !ok {
		return errors.New("runner has no private attach support")
	}
	copy := *options
	if _, deployment := l.policy.Deployment(); deployment != "" && deployment != options.DeploymentID {
		return errors.New("protected Job deployment identity changed")
	}
	resources, err := controlpolicy.CanonicalApplications(options.Resources, nil)
	if err != nil {
		return err
	}
	copy.Resources = resources
	l.protected = &copy
	return nil
}

func (l *lifecycle) protectedSelection(ctx context.Context, w jobs.Workload, policy string) (*privateio.JobOpen, error) {
	if w.Resource == "" {
		return nil, nil
	}
	if l.protected == nil || w.RequestID == "" || w.InputPath != "" || w.MaxOutputBytes != 0 || w.Network != jobs.NetworkNone && w.Network != jobs.NetworkApplication {
		return nil, errors.New("protected Job capability unavailable")
	}
	repository, err := l.protected.Repository(ctx, w.CWD)
	if err != nil {
		return nil, err
	}
	for _, resource := range l.protected.Resources {
		if resource.Alias == w.Resource && resource.Authorize(l.protected.Principal, repository, w.Environment, "execute") == nil && (w.Network == jobs.NetworkNone || resource.Network == "application") {
			var destinations []string
			if w.Network == jobs.NetworkApplication {
				destinations = append([]string(nil), resource.Destinations...)
			}
			return &privateio.JobOpen{Network: string(w.Network), Destinations: destinations, CWD: w.CWD, Protocol: 1, JobID: w.ID, DeploymentID: l.protected.DeploymentID, RequestID: w.RequestID, RequestSHA256: w.RequestSHA256, PolicySHA256: policy, Principal: l.protected.Principal, Repository: repository, Resource: resource.Alias, Environment: w.Environment, Argv: append([]string(nil), w.Argv...)}, nil
		}
	}
	return nil, errors.New("application resource is not authorized")
}

type journalSink struct {
	l  *lifecycle
	id string
}

func (s journalSink) Write(p []byte) (int, error) {
	if err := s.l.journal.AppendMaskedOutput(s.id, p, s.l.now()); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (l *lifecycle) runProtected(id string, plan sandbox.Plan, parent context.Context, job *ownedJob) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	runner := l.runner.(protectedRunner)
	started, startErr := runner.PrepareProtectedJob(ctx, plan)
	bound := false
	if started.InstanceRef != "" {
		_, err := l.journal.BindInstance(id, started.InstanceRef, l.now())
		bound = err == nil
		startErr = errors.Join(startErr, err)
	}
	fail := func(cause error) {
		cancel()
		cleanup := jobs.CleanupNotRequired
		if started.Created {
			cleanup = jobs.CleanupFailed
			if bound && started.Resource.Valid() {
				status, err := l.runner.CleanupJob(context.Background(), started.Resource, started.InstanceRef)
				cleanup = terminalCleanup(status, err)
			}
		}
		outcome := jobs.OutcomeUnknown
		if !started.Started {
			outcome = jobs.OutcomeLaunchFailed
		}
		if errors.Is(parent.Err(), context.Canceled) {
			outcome = jobs.OutcomeCanceled
		}
		if errors.Is(parent.Err(), context.DeadlineExceeded) {
			outcome = jobs.OutcomeTimedOut
		}
		_, err := l.journal.MarkTerminal(id, jobs.Result{Outcome: outcome, Cleanup: cleanup}, l.now())
		job.err = errors.Join(cause, err)
	}
	if startErr != nil || !bound {
		fail(startErr)
		return
	}
	if _, err := l.journal.BindEndpointLeases(id, jobEndpointLeases(started.EndpointBindings), l.now()); err != nil {
		fail(err)
		return
	}
	attachment, err := runner.AttachProtectedJob(ctx, started.Resource, started.InstanceRef)
	if err != nil {
		fail(err)
		return
	}
	defer attachment.Close()
	stopAttach := context.AfterFunc(ctx, func() { attachment.Close() })
	defer stopAttach()
	open := *job.protected
	base, baseErr := attachment.BootstrapEnvironment(plan)
	if baseErr != nil {
		fail(baseErr)
		return
	}
	open.BaseEnvironment = base
	observed, resolveErr := l.protected.Repository(ctx, open.CWD)
	if resolveErr != nil || observed != open.Repository || open.PolicySHA256 != plan.PolicySHA256() {
		fail(errors.New("protected Job repository or policy changed"))
		return
	}
	open.InstanceRef = started.InstanceRef
	session, err := privateio.OpenJob(ctx, l.protected.Socket, l.protected.BrokerUID, open, func(input privateio.PreparedJob) error {
		_, err := l.journal.BindProtectedInput(id, input, l.now())
		return err
	})
	if err != nil {
		fail(err)
		return
	}
	defer session.Close()
	if err = session.ForwardBootstrap(func() error { return attachment.Start(ctx) }, attachment); err != nil {
		fail(err)
		return
	}
	started.Started = true
	report := func(fact privateio.JobFact) error {
		return session.Report(fact, func(intent privateio.ReportIntent) error {
			return l.journal.RecordEngineReport(id, intent, nil, l.now())
		}, func(intent privateio.ReportIntent, result privateio.ReportResult) error {
			return l.journal.RecordEngineReport(id, intent, &result, l.now())
		})
	}
	if err = report(privateio.JobFact{State: "started", ExternalRef: privateio.ExternalReference(started.InstanceRef)}); err != nil {
		fail(err)
		return
	}
	if _, err = l.journal.MarkRunning(id, l.now()); err != nil {
		fail(err)
		return
	}
	rawDone := make(chan error, 1)
	filteredDone := make(chan error, 1)
	go func() {
		err := attachment.Streams(session.Stdout(), session.Stderr())
		if err == nil {
			err = session.EndRaw()
		}
		if err != nil {
			cancel()
		}
		rawDone <- err
	}()
	go func() {
		err := session.Collect(journalSink{l, id})
		if err == nil {
			err = l.journal.MarkFilteredEOF(id, l.now())
		}
		if err != nil {
			cancel()
		}
		filteredDone <- err
	}()
	var result sandbox.Result
	var observeErr error
	for {
		result, observeErr = l.runner.ObserveJob(ctx, started.Resource, started.InstanceRef)
		if terminalSandboxResult(result) || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(reconcileRetryDelay):
		}
	}
	if ctx.Err() != nil {
		attachment.Close()
		session.Close()
	}
	rawErr, filteredErr := <-rawDone, <-filteredDone
	if rawErr != nil || filteredErr != nil {
		fail(errors.Join(rawErr, filteredErr, observeErr))
		return
	}
	if !terminalSandboxResult(result) {
		fail(observeErr)
		return
	}
	fact := privateio.JobFact{State: "unknown", Reason: "outcome_unavailable", ExternalRef: privateio.ExternalReference(started.InstanceRef)}
	if result.ExitCodeKnown {
		exit := result.ExitCode
		fact.State = "finished"
		fact.Reason = ""
		fact.ExitCode = &exit
	}
	if err = report(fact); err != nil {
		fail(err)
		return
	}
	_, job.err = l.persistTerminal(id, started.Resource, started.InstanceRef, result)
}
