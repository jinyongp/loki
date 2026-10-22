//go:build linux

// Package secretbroker owns the cooperative same-user receiver and filtered
// I/O service. Docker authority and durable Job state stay in the launcher.
package secretbroker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"loki/internal/platform/privateio"
	"loki/internal/secretoutput"
)

var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var keyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
var errWire = errors.New("private receiver rejected input")

// Address is obtained from the pinned CLI's values runner address operation;
// Loki neither derives its endpoint nor reads the provider's vault files.
type Address struct {
	RootID            string `json:"root_id"`
	VaultID           string `json:"vault_id"`
	RunnerID          string `json:"runner_id"`
	InstanceID        string `json:"instance_id"`
	Endpoint          string `json:"endpoint"`
	Protocol          int    `json:"protocol"`
	RedactionProtocol int    `json:"redaction_protocol"`
}

func (a Address) valid() bool {
	return digestPattern.MatchString(a.RootID) && idPattern.MatchString(a.VaultID) && uuidPattern.MatchString(a.RunnerID) && uuidPattern.MatchString(a.InstanceID) && a.Protocol == 1 && a.RedactionProtocol == 1 && filepath.IsAbs(a.Endpoint) && filepath.Clean(a.Endpoint) == a.Endpoint && len(a.Endpoint) < 108
}

type bindFrame struct {
	Protocol            int    `json:"protocol"`
	Type                string `json:"type"`
	RootID              string `json:"root_id"`
	VaultID             string `json:"vault_id"`
	RunnerID            string `json:"runner_id"`
	InstanceID          string `json:"instance_id"`
	ServerChallenge     string `json:"server_challenge"`
	ClientChallenge     string `json:"client_challenge"`
	ExecutionID         string `json:"execution_id,omitempty"`
	InputID             string `json:"input_id,omitempty"`
	ExternalExecutionID string `json:"external_execution_id,omitempty"`
}

type InputIdentity struct{ ExecutionID, InputID, ExternalExecutionID, DeliveryID string }
type wireValue struct {
	Key   string  `json:"key"`
	Kind  string  `json:"kind"`
	Value *string `json:"value"`
}
type payloadWire struct {
	Protocol            int         `json:"protocol"`
	Type                string      `json:"type"`
	ExecutionID         string      `json:"execution_id"`
	InputID             string      `json:"input_id"`
	DeliveryID          string      `json:"delivery_id"`
	ExternalExecutionID string      `json:"external_execution_id"`
	Values              []wireValue `json:"values"`
	Redaction           struct {
		Protocol int      `json:"protocol"`
		Patterns [][]byte `json:"patterns"`
	} `json:"redaction"`
}

// ReceivedInput is transient private state. All ordinary formatting and
// serialization refuse values and patterns. Only the accepted handler owns it.
type ReceivedInput struct {
	wire    payloadWire
	matcher *secretoutput.Matcher
}

func (ReceivedInput) Format(s fmt.State, _ rune)   { _, _ = io.WriteString(s, "[REDACTED]") }
func (ReceivedInput) MarshalJSON() ([]byte, error) { return nil, errWire }
func (ReceivedInput) MarshalText() ([]byte, error) { return nil, errWire }
func (p *ReceivedInput) Identity() InputIdentity {
	return InputIdentity{p.wire.ExecutionID, p.wire.InputID, p.wire.ExternalExecutionID, p.wire.DeliveryID}
}

// WithValues is a broker-local authorization/bootstrap boundary; callers must
// not retain the borrowed map. No public transport receives this method.
func (p *ReceivedInput) WithValues(f func(values map[string]string, kinds map[string]string) error) error {
	values, kinds := map[string]string{}, map[string]string{}
	for _, v := range p.wire.Values {
		values[v.Key] = *v.Value
		kinds[v.Key] = v.Kind
	}
	defer clear(values)
	defer clear(kinds)
	return f(values, kinds)
}
func (p *ReceivedInput) Writer(destination io.Writer) *secretoutput.Writer {
	return secretoutput.NewWriter(destination, p.matcher)
}
func (p *ReceivedInput) destroy() {
	if p.matcher != nil {
		p.matcher.Destroy()
	}
	for _, pattern := range p.wire.Redaction.Patterns {
		clear(pattern)
	}
	clear(p.wire.Redaction.Patterns)
	clear(p.wire.Values)
}

func decodePayload(body []byte, b bindFrame) (*ReceivedInput, error) {
	p := &ReceivedInput{}
	if err := privateio.Decode(body, &p.wire); err != nil {
		p.destroy()
		return nil, errWire
	}
	w := &p.wire
	invalid := func() (*ReceivedInput, error) { p.destroy(); return nil, errWire }
	if w.Protocol != 1 || w.Type != "payload" || w.ExecutionID != b.ExecutionID || w.InputID != b.InputID || w.ExternalExecutionID != b.ExternalExecutionID || !idPattern.MatchString(w.DeliveryID) || w.Values == nil || len(w.Values) > 256 || w.Redaction.Protocol != 1 || w.Redaction.Patterns == nil {
		return invalid()
	}
	seen := map[string]bool{}
	patternIndex := 0
	for _, v := range w.Values {
		if !keyPattern.MatchString(v.Key) || seen[v.Key] || v.Value == nil || !utf8.ValidString(*v.Value) || strings.ContainsRune(*v.Value, 0) || v.Kind != "secret" && v.Kind != "variable" {
			return invalid()
		}
		seen[v.Key] = true
		if v.Kind == "secret" && *v.Value != "" {
			if patternIndex >= len(w.Redaction.Patterns) || !bytes.Equal(w.Redaction.Patterns[patternIndex], []byte(*v.Value)) {
				return invalid()
			}
			patternIndex++
		}
	}
	if patternIndex != len(w.Redaction.Patterns) {
		return invalid()
	}
	var err error
	p.matcher, err = secretoutput.New(w.Redaction.Patterns)
	if err != nil {
		return invalid()
	}
	return p, nil
}

type Receiver struct {
	Address Address
	// Admit must verify the already-durable launcher reservation and exact Job
	// binding. Accept additionally validates actual values against resource keys.
	Admit  func(context.Context, InputIdentity) error
	Accept func(context.Context, *ReceivedInput) error
	Handle func(context.Context, *ReceivedInput) error
}

// Serve owns only the supplied listener. The service installs its private
// endpoint separately; it never unlinks a live listener to replace another run.
func (r Receiver) Serve(ctx context.Context, listener *Listener) error {
	if !r.Address.valid() || listener == nil || listener.Addr().String() != r.Address.Endpoint {
		return errWire
	}
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()
	defer func() { cancel(); wg.Wait() }()
	capacity := make(chan struct{}, 8)
	for {
		select {
		case capacity <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		conn, err := listener.AcceptUnix()
		if err != nil {
			<-capacity
			return err
		}
		if privateio.VerifyPeer(conn, uint32(os.Getuid())) != nil {
			_ = conn.Close()
			<-capacity
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-capacity }()
			defer conn.Close()
			defer func() {
				if recover() != nil {
					cancel()
				}
			}()
			_ = r.receive(ctx, conn)
		}()
	}
}

func (r Receiver) receive(ctx context.Context, conn *net.UnixConn) error {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return errWire
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return errWire
	}
	server := hex.EncodeToString(nonce[:])
	a := r.Address
	hello := map[string]any{"protocol": 1, "type": "hello", "root_id": a.RootID, "vault_id": a.VaultID, "runner_id": a.RunnerID, "instance_id": a.InstanceID, "server_challenge": server}
	if err := privateio.WriteControl(conn, hello); err != nil {
		return errWire
	}
	var b bindFrame
	if privateio.ReadControl(conn, &b) != nil || b.Protocol != 1 || b.RootID != a.RootID || b.VaultID != a.VaultID || b.RunnerID != a.RunnerID || b.InstanceID != a.InstanceID || b.ServerChallenge != server || !idPattern.MatchString(b.ClientChallenge) {
		return errWire
	}
	ready := map[string]any{"protocol": 1, "type": "ready", "server_challenge": server, "client_challenge": b.ClientChallenge, "redaction_protocol": 1}
	if b.Type == "probe" {
		if b.ExecutionID != "" || b.InputID != "" || b.ExternalExecutionID != "" {
			return errWire
		}
		ready["type"] = "registered"
		return privateio.WriteControl(conn, ready)
	}
	if b.Type != "bind" || !idPattern.MatchString(b.ExecutionID) || !idPattern.MatchString(b.InputID) || !idPattern.MatchString(b.ExternalExecutionID) || r.Admit == nil || r.Accept == nil || r.Handle == nil {
		return errWire
	}
	if err := r.Admit(ctx, InputIdentity{b.ExecutionID, b.InputID, b.ExternalExecutionID, ""}); err != nil {
		return errWire
	}
	if err := privateio.WriteControl(conn, ready); err != nil {
		return errWire
	}
	body, err := privateio.ReadBytes(conn, privateio.MaxPayloadFrame)
	if err != nil {
		return errWire
	}
	defer clear(body)
	p, err := decodePayload(body, b)
	if err != nil {
		return errWire
	}
	defer p.destroy()
	if err = r.Accept(ctx, p); err != nil {
		return errWire
	}
	ack := map[string]any{"protocol": 1, "type": "accepted", "server_challenge": server, "client_challenge": b.ClientChallenge, "execution_id": b.ExecutionID, "input_id": b.InputID, "delivery_id": p.wire.DeliveryID, "redaction_protocol": 1}
	// A lost ACK still must not skip ownership/cleanup in Handle. The provider
	// will report uncertainty and cannot cause another sensitive send.
	_ = privateio.WriteControl(conn, ack)
	_ = conn.Close()
	return r.Handle(ctx, p)
}

// Listen creates a new private endpoint. Caller lifecycle owns stale cleanup;
// this function never removes an existing file/socket or repairs foreign ACLs.
type Listener struct {
	*net.UnixListener
	path           string
	parent, socket os.FileInfo
	once           sync.Once
}

// Close never removes a successor endpoint or follows a replaced parent.
func (l *Listener) Close() error {
	err := l.UnixListener.Close()
	l.once.Do(func() {
		parent, parentErr := os.Lstat(filepath.Dir(l.path))
		socket, socketErr := os.Lstat(l.path)
		if parentErr == nil && socketErr == nil && os.SameFile(parent, l.parent) && os.SameFile(socket, l.socket) {
			_ = os.Remove(l.path)
		}
	})
	return err
}

func Listen(address Address) (*Listener, error) {
	if !address.valid() {
		return nil, errWire
	}
	return listenPrivate(address.Endpoint)
}

func listenPrivate(endpoint string) (*Listener, error) {
	if !filepath.IsAbs(endpoint) || filepath.Clean(endpoint) != endpoint || len(endpoint) >= 108 {
		return nil, errWire
	}
	dir := filepath.Dir(endpoint)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, errWire
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved != dir {
		return nil, errWire
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode() != os.ModeDir|0700 {
		return nil, errWire
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errWire
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != uint32(os.Getuid()) {
		return nil, errWire
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: endpoint, Net: "unix"})
	if err != nil {
		return nil, errWire
	}
	listener.SetUnlinkOnClose(false)
	if err = os.Chmod(endpoint, 0600); err != nil {
		listener.Close()
		return nil, errWire
	}
	socket, err := os.Lstat(endpoint)
	parent, parentErr := os.Lstat(dir)
	if err != nil || parentErr != nil || !os.SameFile(info, parent) || socket.Mode() != os.ModeSocket|0600 {
		listener.Close()
		return nil, errWire
	}
	return &Listener{UnixListener: listener, path: endpoint, parent: parent, socket: socket}, nil
}
