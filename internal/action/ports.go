package action

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"regexp"
	"strconv"
	"sync"
	"time"

	"loki/internal/fault"
	"loki/internal/secret"
)

const portHold = 30 * time.Second
const maxPreparedLaunches = 1024

var launchTokenPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var developmentPorts = portRegistry{held: make(map[int]*portLease), now: time.Now}

type portLease struct {
	port    int
	expires time.Time
}

// Like the reference runtime, this is a short-lived allocation registry, not a
// socket held open until the child binds. Other host processes can still race a
// launch; its process status, rather than allocation, establishes startup success.
type portRegistry struct {
	mu   sync.Mutex
	held map[int]*portLease
	now  func() time.Time
}

func (p *portRegistry) allocate(preferred int) (*portLease, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for port, lease := range p.held {
		if !now.Before(lease.expires) {
			delete(p.held, port)
		}
	}
	for attempt := range 33 {
		candidate := 0
		if attempt == 0 {
			candidate = preferred
		}
		listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: candidate})
		if err != nil {
			continue
		}
		port := listener.Addr().(*net.TCPAddr).Port
		listener.Close()
		if port == 8765 || port == 8766 || port == 8767 || p.held[port] != nil {
			continue
		}
		lease := &portLease{port: port, expires: now.Add(portHold)}
		p.held[port] = lease
		return lease, nil
	}
	return nil, fault.Error("unable to allocate a loopback development port")
}

func (p *portRegistry) release(lease *portLease) {
	if lease == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	// A stale token must not release a newer reservation of the same port.
	if p.held[lease.port] == lease {
		delete(p.held, lease.port)
	}
}

type preparedAction struct {
	profile, action, cwd string
	lease                *portLease
}

func localURL(port int) string { return "http://127.0.0.1:" + strconv.Itoa(port) }

// Caller holds Runtime.mu, serializing token consumption and singleton reuse.
func (r *Runtime) pruneLaunches() {
	now := r.ports.now()
	for token, launch := range r.launches {
		if !now.Before(launch.lease.expires) {
			delete(r.launches, token)
			r.ports.release(launch.lease)
		}
	}
}

func (r *Runtime) reserve(plan secret.ActionPlan) (string, *portLease, error) {
	r.pruneLaunches()
	if len(r.launches) >= maxPreparedLaunches {
		return "", nil, fault.Error("too many prepared action launches; retry after existing launches expire")
	}
	lease, err := r.ports.allocate(plan.Policy.DynamicPort.Preferred)
	if err != nil {
		return "", nil, err
	}
	var bytes [16]byte
	if _, err = rand.Read(bytes[:]); err != nil {
		r.ports.release(lease)
		return "", nil, err
	}
	token := hex.EncodeToString(bytes[:])
	r.launches[token] = preparedAction{plan.Profile, plan.Action, plan.CWD, lease}
	return token, lease, nil
}

func (r *Runtime) consume(token string, plan secret.ActionPlan) (*portLease, error) {
	if !launchTokenPattern.MatchString(token) {
		return nil, fault.Error("invalid action launch token")
	}
	launch, exists := r.launches[token]
	delete(r.launches, token)
	if !exists {
		return nil, fault.Error("action launch token is unknown or expired")
	}
	if !r.ports.now().Before(launch.lease.expires) {
		r.ports.release(launch.lease)
		return nil, fault.Error("action launch token is unknown or expired")
	}
	if launch.profile != plan.Profile || launch.action != plan.Action || launch.cwd != plan.CWD {
		r.ports.release(launch.lease)
		return nil, fault.Error("action launch token does not match the action")
	}
	// Keep the allocation exclusion through startup, including a prepared launch.
	// It expires without a timer and is released immediately on a failed launch.
	return launch.lease, nil
}
