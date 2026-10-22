//go:build linux

package secretbroker

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	controlpolicy "loki/internal/control/policy"
	"loki/internal/platform/privateio"
	provider "loki/modules/secrets/devtools"
)

type Options struct {
	Provider        *provider.Provider
	Socket          string
	LauncherUID     uint32
	DeploymentID    string
	PolicySHA256    string
	RunnerID        string
	InstanceID      string
	Resources       []controlpolicy.ApplicationResource
	BaseEnvironment map[string]string
}

type Service struct {
	options  Options
	mu       sync.Mutex
	jobs     map[string]*protectedJob
	initMu   sync.Mutex
	address  Address
	receiver *Listener
}

type protectedJob struct {
	execution  provider.Execution
	delivered  chan struct{}
	open       privateio.JobOpen
	resource   controlpolicy.ApplicationResource
	prepared   privateio.PreparedJob
	connection *net.UnixConn
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan error
	accepted   bool
	committed  bool
}

func New(options Options) (*Service, error) {
	if options.Provider == nil || options.LauncherUID == uint32(os.Getuid()) || options.DeploymentID == "" || !digestPattern.MatchString(options.PolicySHA256) || !uuidPattern.MatchString(options.RunnerID) || !uuidPattern.MatchString(options.InstanceID) {
		return nil, errWire
	}
	resources, err := controlpolicy.CanonicalApplications(options.Resources, options.BaseEnvironment)
	if err != nil {
		return nil, errWire
	}
	options.Resources = resources
	base := map[string]string{}
	for name, value := range options.BaseEnvironment {
		base[name] = value
	}
	options.BaseEnvironment = base
	return &Service{options: options, jobs: map[string]*protectedJob{}}, nil
}

// Serve exposes only the launcher Job channel. Metadata and administration use
// separate typed role sockets; this socket cannot select CLI commands or roots.
func (s *Service) Serve(ctx context.Context) error {
	listener, err := listenPrivate(s.options.Socket)
	if err != nil {
		return errWire
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	stop := context.AfterFunc(ctx, func() {
		listener.Close()
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, job := range s.jobs {
			job.cancel()
			job.connection.Close()
		}
	})
	defer stop()
	defer func() {
		s.initMu.Lock()
		defer s.initMu.Unlock()
		if s.receiver != nil {
			s.receiver.Close()
		}
	}()
	capacity := make(chan struct{}, provider.MaxProtectedJobs)
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			return err
		}
		if privateio.VerifyPeer(conn, s.options.LauncherUID) != nil {
			conn.Close()
			continue
		}
		select {
		case capacity <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-capacity }()
			defer conn.Close()
			stopConnection := context.AfterFunc(ctx, func() { conn.Close() })
			defer stopConnection()
			s.serveJob(ctx, conn)
		}()
	}
}

func (s *Service) ensureReceiver(ctx context.Context) error {
	s.initMu.Lock()
	defer s.initMu.Unlock()
	if s.receiver != nil {
		return nil
	}
	a, err := s.options.Provider.Address(ctx, s.options.RunnerID, s.options.InstanceID)
	if err != nil {
		return errWire
	}
	address := Address(a)
	listener, err := Listen(address)
	if err != nil {
		return errWire
	}
	r := Receiver{Address: address, Admit: s.admit, Accept: s.accept, Handle: s.handle}
	go func() { _ = r.Serve(ctx, listener) }()
	request := privateio.OperationID(s.options.DeploymentID, s.options.RunnerID, s.options.InstanceID+"/register")
	if s.options.Provider.Register(ctx, s.options.RunnerID, s.options.InstanceID, request) != nil {
		listener.Close()
		return errWire
	}
	s.address, s.receiver = address, listener
	return nil
}

func (s *Service) authorize(open privateio.JobOpen) (controlpolicy.ApplicationResource, error) {
	if open.Protocol != 1 || !idPattern.MatchString(open.JobID) || open.DeploymentID != s.options.DeploymentID || !uuidPattern.MatchString(open.RequestID) || !digestPattern.MatchString(open.RequestSHA256) || open.PolicySHA256 != s.options.PolicySHA256 || open.InstanceRef == "" || open.BaseEnvironment == nil {
		return controlpolicy.ApplicationResource{}, errWire
	}
	base := map[string]string{}
	for _, entry := range open.BaseEnvironment {
		if _, exists := base[entry.Name]; exists {
			return controlpolicy.ApplicationResource{}, errWire
		}
		base[entry.Name] = entry.Value
	}
	if open.Network != "none" && open.Network != "application" {
		return controlpolicy.ApplicationResource{}, errWire
	}
	extra := 0
	if open.Network == "application" {
		extra = 4
	}
	if len(base) != len(s.options.BaseEnvironment)+extra {
		return controlpolicy.ApplicationResource{}, errWire
	}
	for name, value := range s.options.BaseEnvironment {
		if actual, ok := base[name]; !ok || actual != value {
			return controlpolicy.ApplicationResource{}, errWire
		}
	}
	if open.Network == "application" {
		proxy, err := url.Parse(base["HTTPS_PROXY"])
		if err != nil || proxy.Scheme != "http" || proxy.User == nil || proxy.User.Username() != "loki" || proxy.Path != "" || proxy.RawQuery != "" || proxy.Fragment != "" || proxy.Opaque != "" || base["HTTPS_PROXY"] != base["https_proxy"] || base["NO_PROXY"] != "localhost,127.0.0.1,::1" || base["no_proxy"] != base["NO_PROXY"] {
			return controlpolicy.ApplicationResource{}, errWire
		}
		password, ok := proxy.User.Password()
		port, err := strconv.Atoi(proxy.Port())
		ip := net.ParseIP(proxy.Hostname())
		if !ok || len(password) != 43 || err != nil || port < 1024 || port > 65535 || ip == nil || ip.To4() == nil {
			return controlpolicy.ApplicationResource{}, errWire
		}
	}
	for _, r := range s.options.Resources {
		if r.Alias == open.Resource && r.Authorize(open.Principal, open.Repository, open.Environment, "execute") == nil && (open.Network == "none" && len(open.Destinations) == 0 || open.Network == "application" && r.Network == "application" && slices.Equal(open.Destinations, r.Destinations)) {
			return r, nil
		}
	}
	return controlpolicy.ApplicationResource{}, errWire
}

func (s *Service) serveJob(serviceCtx context.Context, conn *net.UnixConn) {
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	var request privateio.JobRequest
	if privateio.ReadControl(conn, &request) != nil {
		return
	}
	if request.Reconcile != nil && request.Open == nil {
		s.reconcileJob(serviceCtx, conn, *request.Reconcile)
		return
	}
	if request.Open == nil || request.Reconcile != nil {
		return
	}
	open := *request.Open
	resource, err := s.authorize(open)
	if err != nil {
		return
	}
	if s.ensureReceiver(serviceCtx) != nil {
		return
	}
	ctx, cancel := context.WithCancel(serviceCtx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	job := &protectedJob{open: open, resource: resource, connection: conn, ctx: ctx, cancel: cancel, done: make(chan error, 1), delivered: make(chan struct{})}
	s.mu.Lock()
	if _, exists := s.jobs[open.JobID]; exists {
		s.mu.Unlock()
		return
	}
	s.jobs[open.JobID] = job
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.jobs, open.JobID); s.mu.Unlock() }()
	keys := make([]string, 0, len(resource.Keys))
	for name := range resource.Keys {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	env := open.Environment
	if env == "common" {
		env = ""
	}
	op := func(name string) string { return privateio.OperationID(open.DeploymentID, open.JobID, name) }
	execution, err := s.options.Provider.Prepare(ctx, resource.Profile(), env, open.JobID, op("prepare"), resource.Bundle, keys)
	if err != nil || execution.JobID != open.JobID {
		return
	}
	s.mu.Lock()
	job.prepared = privateio.PreparedJob{Stage: "prepared", ExecutionID: execution.ID, InputID: execution.InputID, RunnerID: s.options.RunnerID, RunnerInstanceID: s.options.InstanceID, Revision: execution.Revision, PrepareRequestID: op("prepare"), ApproveRequestID: op("approve"), DeliverRequestID: op("deliver")}
	s.mu.Unlock()
	if privateio.WriteControl(conn, job.prepared) != nil {
		return
	}
	var commit privateio.CommitJob
	if privateio.ReadControl(conn, &commit) != nil || commit.Stage != "prepared" || commit.JobID != open.JobID || commit.ExecutionID != execution.ID || commit.InputID != execution.InputID {
		return
	}
	execution, err = s.options.Provider.Approve(ctx, resource.Profile(), s.options.RunnerID, s.options.InstanceID, op("approve"), execution)
	if err != nil {
		return
	}
	s.mu.Lock()
	job.prepared.Stage = "approved"
	job.prepared.Revision = execution.Revision
	s.mu.Unlock()
	if privateio.WriteControl(conn, job.prepared) != nil {
		return
	}
	if privateio.ReadControl(conn, &commit) != nil || commit.Stage != "approved" || commit.JobID != open.JobID || commit.ExecutionID != execution.ID || commit.InputID != execution.InputID {
		return
	}
	s.mu.Lock()
	job.committed = true
	s.mu.Unlock()
	// Only the original recorded delivery UUID is used. Errors never cause a
	// second prepare, input selection or send attempt.
	execution, err = s.options.Provider.Deliver(ctx, resource.Profile(), op("deliver"), execution)
	job.execution = execution
	close(job.delivered)
	if err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	select {
	case <-job.done:
	case <-ctx.Done():
	}
}

func (s *Service) lookup(identity InputIdentity) (*protectedJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[identity.ExternalExecutionID]
	if job == nil || !job.committed || job.prepared.ExecutionID != identity.ExecutionID || job.prepared.InputID != identity.InputID || job.ctx.Err() != nil {
		return nil, errWire
	}
	return job, nil
}

func (s *Service) admit(_ context.Context, identity InputIdentity) error {
	_, err := s.lookup(identity)
	return err
}

func (s *Service) bootstrap(job *protectedJob, p *ReceivedInput) ([]byte, error) {
	input := privateio.Bootstrap{Protocol: 1, Environment: append([]privateio.EnvironmentEntry(nil), job.open.BaseEnvironment...)}
	defer clear(input.Environment)
	err := p.WithValues(func(values, kinds map[string]string) error {
		if len(values) != len(job.resource.Keys) {
			return errWire
		}
		keys := make([]string, 0, len(values))
		for name := range values {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		base := map[string]string{}
		for _, entry := range input.Environment {
			base[entry.Name] = entry.Value
		}
		for _, name := range keys {
			if job.resource.Keys[name] != kinds[name] || values[name] == "" || controlpolicy.ReservedApplicationKey(name, base) {
				return errWire
			}
			input.Environment = append(input.Environment, privateio.EnvironmentEntry{Name: name, Value: values[name]})
		}
		return nil
	})
	if err != nil {
		return nil, errWire
	}
	// Protected OCI plans fix the stack at8MiB on supported64bit Linux.
	validated, err := privateio.ValidateBootstrap(input, job.open.Argv, 8<<20, os.Getpagesize())
	clear(validated)
	if err != nil {
		return nil, errWire
	}
	body, err := json.Marshal(input)
	if err != nil || len(body) > privateio.MaxPayloadFrame {
		clear(body)
		return nil, errWire
	}
	return body, nil
}

func (s *Service) accept(_ context.Context, p *ReceivedInput) error {
	job, err := s.lookup(p.Identity())
	if err != nil {
		return err
	}
	body, err := s.bootstrap(job, p)
	clear(body)
	if err != nil {
		return errWire
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if job.accepted {
		return errWire
	}
	job.accepted = true
	return nil
}

type maskedStream struct {
	conn   io.Writer
	stream byte
}

func (w maskedStream) Write(p []byte) (int, error) {
	for remaining := p; len(remaining) > 0; {
		n := min(len(remaining), 64<<10)
		frame := make([]byte, n+1)
		frame[0] = w.stream
		copy(frame[1:], remaining[:n])
		err := privateio.WriteBytes(w.conn, frame, (64<<10)+1)
		clear(frame)
		if err != nil {
			return 0, errWire
		}
		remaining = remaining[n:]
	}
	return len(p), nil
}

func (s *Service) handle(_ context.Context, p *ReceivedInput) (err error) {
	job, err := s.lookup(p.Identity())
	if err != nil {
		return err
	}
	defer func() { job.done <- err }()
	body, err := s.bootstrap(job, p)
	if err != nil {
		return err
	}
	defer clear(body)
	if privateio.WriteBytes(job.connection, body, privateio.MaxPayloadFrame) != nil {
		return errWire
	}
	select {
	case <-job.delivered:
	case <-job.ctx.Done():
		return errWire
	}
	if job.execution.ID == "" {
		return errWire
	}
	if err = s.report(job, "started"); err != nil {
		return err
	}
	stdout := p.Writer(maskedStream{job.connection, privateio.StreamStdout})
	stderr := p.Writer(maskedStream{job.connection, privateio.StreamStderr})
	defer stdout.Abort()
	defer stderr.Abort()
	for {
		frame, err := privateio.ReadBytes(job.connection, (64<<10)+1)
		if err != nil {
			return errWire
		}
		stream := frame[0]
		if stream == privateio.StreamEOF && len(frame) == 1 {
			clear(frame)
			if stdout.Close() != nil || stderr.Close() != nil {
				return errWire
			}
			if privateio.WriteBytes(job.connection, []byte{privateio.StreamEOF}, 1) != nil {
				return errWire
			}
			return s.report(job, "terminal")
		}
		var writer io.Writer
		switch stream {
		case privateio.StreamStdout:
			writer = stdout
		case privateio.StreamStderr:
			writer = stderr
		default:
			clear(frame)
			return errWire
		}
		_, err = writer.Write(frame[1:])
		clear(frame)
		if err != nil {
			return errWire
		}
	}
}

func (s *Service) report(job *protectedJob, phase string) error {
	var fact privateio.JobFact
	if privateio.ReadControl(job.connection, &fact) != nil || fact.ExternalRef != privateio.ExternalReference(job.open.InstanceRef) {
		return errWire
	}
	if phase == "started" {
		if fact.State != "started" || fact.ExitCode != nil || fact.Reason != "" {
			return errWire
		}
	} else {
		if fact.State == "finished" {
			if fact.ExitCode == nil || *fact.ExitCode < 0 || *fact.ExitCode > 255 || fact.Reason != "" {
				return errWire
			}
		} else if fact.State != "unknown" || fact.ExitCode != nil || fact.Reason != "outcome_unavailable" {
			return errWire
		}
	}
	request := privateio.OperationID(job.open.DeploymentID, job.open.JobID, "report-"+fact.State)
	intent := privateio.ReportIntent{RequestID: request, Revision: job.execution.Revision, Fact: fact}
	if privateio.WriteControl(job.connection, intent) != nil {
		return errWire
	}
	var commit struct {
		RequestID string `json:"request_id"`
	}
	if privateio.ReadControl(job.connection, &commit) != nil || commit.RequestID != request {
		return errWire
	}
	ctx, stop := context.WithTimeout(job.ctx, 2*time.Second)
	defer stop()
	execution, err := s.options.Provider.Report(ctx, job.resource.Profile(), request, s.options.RunnerID, job.execution, provider.EngineFact{State: fact.State, ExternalRef: fact.ExternalRef, ExitCode: fact.ExitCode, Reason: fact.Reason})
	result := privateio.ReportResult{}
	if err == nil {
		job.execution = execution
		result.Recorded = true
		result.Revision = execution.Revision
	}
	// A locked vault cannot revoke delivered bytes or their matcher. Retain
	// the original report intent in the launcher journal for reconciliation.
	return privateio.WriteControl(job.connection, result)
}
