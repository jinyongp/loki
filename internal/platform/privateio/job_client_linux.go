//go:build linux

package privateio

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

var upstreamID = regexp.MustCompile(`^[0-9a-f]{32}$`)
var runnerID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type JobSession struct {
	open JobOpen
	conn *net.UnixConn
	mu   sync.Mutex
	stop func() bool
}

// OpenJob requires fsync of the original metadata binding before approving any
// private send. Authentication is mutual and independent of socket permissions.
func OpenJob(ctx context.Context, socket string, expectedUID uint32, open JobOpen, persist func(PreparedJob) error) (*JobSession, error) {
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket || persist == nil {
		return nil, ErrInvalid
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, ErrInvalid
	}
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		conn.Close()
		return nil, ErrInvalid
	}
	session := &JobSession{conn: unixConn, open: open}
	session.stop = context.AfterFunc(ctx, func() { unixConn.Close() })
	fail := func() (*JobSession, error) { session.Close(); return nil, ErrInvalid }
	_ = unixConn.SetDeadline(time.Now().Add(30 * time.Second))
	if VerifyPeer(unixConn, expectedUID) != nil || WriteControl(unixConn, open) != nil {
		return fail()
	}
	var previous PreparedJob
	for _, stage := range []string{"prepared", "approved"} {
		var prepared PreparedJob
		if ReadControl(unixConn, &prepared) != nil || !upstreamID.MatchString(prepared.ExecutionID) || !upstreamID.MatchString(prepared.InputID) || !runnerID.MatchString(prepared.RunnerID) || !runnerID.MatchString(prepared.RunnerInstanceID) || prepared.Revision == 0 || prepared.PrepareRequestID != OperationID(open.DeploymentID, open.JobID, "prepare") || prepared.ApproveRequestID != OperationID(open.DeploymentID, open.JobID, "approve") || prepared.DeliverRequestID != OperationID(open.DeploymentID, open.JobID, "deliver") {
			return fail()
		}
		if prepared.Stage != stage || stage == "approved" && (prepared.ExecutionID != previous.ExecutionID || prepared.InputID != previous.InputID || prepared.RunnerID != previous.RunnerID || prepared.RunnerInstanceID != previous.RunnerInstanceID || prepared.Revision <= previous.Revision) {
			return fail()
		}
		if persist(prepared) != nil {
			return fail()
		}
		if WriteControl(unixConn, CommitJob{Stage: stage, JobID: open.JobID, ExecutionID: prepared.ExecutionID, InputID: prepared.InputID}) != nil {
			return fail()
		}
		previous = prepared
	}
	return session, nil
}

func (s *JobSession) Close() error { s.stop(); return s.conn.Close() }

func (s *JobSession) Report(fact JobFact, persist func(ReportIntent) error, complete func(ReportIntent, ReportResult) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if persist == nil || complete == nil || WriteControl(s.conn, fact) != nil {
		return ErrInvalid
	}
	var intent ReportIntent
	if ReadControl(s.conn, &intent) != nil || intent.Revision == 0 || intent.RequestID != OperationID(s.open.DeploymentID, s.open.JobID, "report-"+fact.State) || intent.Fact.State != fact.State || intent.Fact.ExternalRef != fact.ExternalRef || intent.Fact.Reason != fact.Reason || (intent.Fact.ExitCode == nil) != (fact.ExitCode == nil) || fact.ExitCode != nil && *intent.Fact.ExitCode != *fact.ExitCode {
		return ErrInvalid
	}
	if persist(intent) != nil {
		return ErrInvalid
	}
	if WriteControl(s.conn, struct {
		RequestID string `json:"request_id"`
	}{intent.RequestID}) != nil {
		return ErrInvalid
	}
	var result ReportResult
	if ReadControl(s.conn, &result) != nil || result.Recorded && result.Revision < intent.Revision {
		return ErrInvalid
	}
	return complete(intent, result)
}

// ForwardBootstrap starts only the trusted bootstrap, then forwards the single
// bounded frame. Project exec waits for the complete validated environment.
func (s *JobSession) ForwardBootstrap(start func() error, destination io.Writer) error {
	body, err := ReadBytes(s.conn, MaxPayloadFrame)
	if err != nil {
		return ErrInvalid
	}
	defer clear(body)
	if start() == nil && WriteBytes(destination, body, MaxPayloadFrame) == nil {
		return s.conn.SetDeadline(time.Time{})
	}
	return ErrInvalid
}

type rawJobStream struct {
	session *JobSession
	stream  byte
}

func (s *JobSession) Stdout() io.Writer { return rawJobStream{s, StreamStdout} }
func (s *JobSession) Stderr() io.Writer { return rawJobStream{s, StreamStderr} }
func (w rawJobStream) Write(p []byte) (int, error) {
	w.session.mu.Lock()
	defer w.session.mu.Unlock()
	for rest := p; len(rest) > 0; {
		n := min(len(rest), 64<<10)
		frame := make([]byte, n+1)
		frame[0] = w.stream
		copy(frame[1:], rest[:n])
		err := WriteBytes(w.session.conn, frame, (64<<10)+1)
		clear(frame)
		if err != nil {
			return 0, ErrInvalid
		}
		rest = rest[n:]
	}
	return len(p), nil
}

func (s *JobSession) EndRaw() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return WriteBytes(s.conn, []byte{StreamEOF}, 1)
}

func (s *JobSession) Collect(destination io.Writer) error {
	for {
		frame, err := ReadBytes(s.conn, (64<<10)+1)
		if err != nil {
			return errors.New("protected collector lost")
		}
		if len(frame) == 1 && frame[0] == StreamEOF {
			return nil
		}
		if len(frame) < 2 || frame[0] != StreamStdout && frame[0] != StreamStderr {
			clear(frame)
			return ErrInvalid
		}
		n, err := destination.Write(frame[1:])
		size := len(frame) - 1
		clear(frame)
		if err != nil || n != size {
			return errors.New("protected output storage failed")
		}
	}
}
