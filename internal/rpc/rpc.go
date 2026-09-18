// Package rpc implements the bounded newline-JSON runtime Unix socket protocol.
package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/control/identity"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/fault"
)

const MaxBytes = 2_097_152

// Limits are local service configuration, never request-controlled.
// Zero values preserve the runtime's 2 MiB / 30 second protocol.
type Limits struct {
	RequestBytes, ResponseBytes int
	Timeout                     time.Duration
}

func (l Limits) normalized() Limits {
	if l.RequestBytes <= 0 {
		l.RequestBytes = MaxBytes
	}
	if l.ResponseBytes <= 0 {
		l.ResponseBytes = MaxBytes
	}
	if l.Timeout <= 0 {
		l.Timeout = 30 * time.Second
	}
	return l
}

type Peer struct {
	PID      int32
	UID, GID uint32
}
type Handler func(context.Context, json.RawMessage) (any, error)
type Operation struct {
	Grant  controlpolicy.Grant
	Handle Handler
	// Timeout is a trusted execution allowance applied only after a bounded
	// request has been read and its peer authorized. Zero keeps service limits.
	Timeout time.Duration
}
type Event struct {
	Operation string
	UID       uint32
	Success   bool
	Profile   *string
}

type Server struct {
	Limits         Limits
	Principals     identity.Resolver
	Operations     map[string]Operation
	Audit          func(Event)
	MaxConnections int
}

func PeerCredentials(conn *net.UnixConn) (Peer, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var cred *unix.Ucred
	var inner error
	err = raw.Control(func(fd uintptr) { cred, inner = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) })
	if err != nil {
		return Peer{}, err
	}
	if inner != nil {
		return Peer{}, inner
	}
	return Peer{cred.Pid, cred.Uid, cred.Gid}, nil
}

func (s *Server) Authorized(peer Peer, grant controlpolicy.Grant) bool {
	if s.Principals == nil {
		return false
	}
	principal := s.Principals.Resolve(peer.PID, peer.UID, peer.GID)
	return controlpolicy.Allows(principal, grant)
}

func (s *Server) Serve(ctx context.Context, listener *net.UnixListener) error {
	limit := s.MaxConnections
	if limit <= 0 {
		limit = 64
	}
	slots := make(chan struct{}, limit)
	var workers sync.WaitGroup
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	defer workers.Wait()
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			conn.Close()
			return nil
		default:
			conn.Close()
			continue
		}
		workers.Go(func() {
			defer func() { <-slots }()
			defer conn.Close()
			cancel := context.AfterFunc(ctx, func() { conn.Close() })
			defer cancel()
			s.handle(ctx, conn)
		})
	}
}

func readFrame(reader io.Reader) ([]byte, error) {
	return readBoundedFrame(reader, MaxBytes)
}
func readBoundedFrame(reader io.Reader, maximum int) ([]byte, error) {
	raw, err := bufio.NewReader(io.LimitReader(reader, int64(maximum)+1)).ReadBytes('\n')
	if len(raw) > maximum {
		return nil, fault.Error("request is too large")
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, fault.Error("Loki runtime returned no response")
	}
	return bytes.TrimSpace(raw), nil
}

type response struct {
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

func (s *Server) handle(ctx context.Context, conn *net.UnixConn) {
	parent := ctx
	limits := s.Limits.normalized()
	ctx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	peer, err := PeerCredentials(conn)
	if err != nil {
		return
	}
	var request struct {
		Operation string `json:"operation"`
	}
	raw, err := readBoundedFrame(conn, limits.RequestBytes)
	if err == nil {
		if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &request) != nil {
			err = fault.Error("request must be an object")
		}
	}
	var result any
	if err == nil {
		op, ok := s.Operations[request.Operation]
		if request.Operation == "" {
			err = fault.Error("operation is required")
		} else if !ok || op.Handle == nil {
			err = fault.Error("unknown operation")
		} else if !s.Authorized(peer, op.Grant) {
			if op.Grant == controlpolicy.HostAdministration {
				err = fault.Error("administrative operations require the host administrator")
			} else {
				err = fault.Error("delegated operation requires the Loki agent user")
			}
		} else {
			if op.Timeout > 0 {
				operationContext, operationCancel := context.WithTimeout(parent, op.Timeout)
				defer operationCancel()
				ctx = operationContext
				deadline, _ := ctx.Deadline()
				conn.SetDeadline(deadline)
			}
			result, err = invoke(ctx, op.Handle, raw)
		}
	}
	if s.Audit != nil {
		event := Event{Operation: request.Operation, UID: peer.UID, Success: err == nil}
		var metadata map[string]json.RawMessage
		if json.Unmarshal(raw, &metadata) == nil {
			text := func(key string) *string {
				var value string
				if json.Unmarshal(metadata[key], &value) != nil || len(value) > 1024 {
					return nil
				}
				return &value
			}
			event.Profile = text("profile")
		}
		s.Audit(event)
	}
	r := response{OK: err == nil, Result: result}
	if err != nil {
		r.Error = fault.Public(err)
		r.Result = nil
	}
	data, marshalErr := json.Marshal(r)
	if marshalErr != nil {
		data = []byte(`{"ok":false,"error":"runtime response encoding failed"}`)
	}
	if len(data)+1 > limits.ResponseBytes {
		data = []byte(`{"ok":false,"error":"response is too large"}`)
	}
	conn.Write(append(data, '\n'))
}

func invoke(ctx context.Context, h Handler, raw json.RawMessage) (result any, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = fault.Error("unexpected runtime failure; run diagnostics and retry")
		}
	}()
	return h(ctx, raw)
}

// Decode extracts the operation-specific typed request before domain execution.
// The transport-owned operation field is removed before strict domain decoding.
func Decode[T any](raw json.RawMessage) (T, error) {
	var value T
	var envelope map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&envelope); err != nil || envelope == nil {
		return value, fault.Error("invalid runtime arguments")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return value, fault.Error("invalid runtime arguments")
	}
	delete(envelope, "operation")
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return value, fault.Error("invalid runtime arguments")
	}
	decoder = json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&value); err != nil {
		return value, fault.Error("invalid runtime arguments")
	}
	return value, nil
}

type Client struct {
	Limits      Limits
	Socket      string
	ExpectedUID *uint32
}

func (c Client) Call(ctx context.Context, request any) (json.RawMessage, error) {
	limits := c.Limits.normalized()
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if len(data)+1 > limits.RequestBytes {
		return nil, fault.Error("request is too large")
	}
	ctx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if c.ExpectedUID != nil {
		peer, err := PeerCredentials(conn.(*net.UnixConn))
		if err != nil {
			return nil, err
		}
		if peer.UID != *c.ExpectedUID {
			return nil, errors.New("runtime socket owner is not trusted")
		}
	}
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if _, err = conn.Write(append(data, '\n')); err != nil {
		return nil, err
	}
	encoded, err := readBoundedFrame(conn, limits.ResponseBytes)
	if err != nil {
		return nil, err
	}
	var r struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if json.Unmarshal(encoded, &r) != nil {
		return nil, errors.New("invalid runtime response")
	}
	if !r.OK {
		return nil, fault.Error(r.Error)
	}
	return r.Result, nil
}
