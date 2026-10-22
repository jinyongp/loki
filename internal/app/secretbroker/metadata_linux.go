//go:build linux

package secretbroker

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/control/appvalues"
	"loki/internal/control/identity"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/fault"
	"loki/internal/platform/privateio"
	"loki/internal/rpc"
	"loki/modules/secrets/application"
)

type MetadataOptions struct {
	Ready     func() error
	Serving   func() bool
	Socket    string
	AgentUID  uint32
	GroupID   uint32
	Principal string
	Values    *application.Service
}

// ServeMetadata grants only the configured agent UID. Root administration is
// deliberately absent from this listener and uses a separate private channel.
func ServeMetadata(ctx context.Context, options MetadataOptions) error {
	if options.AgentUID == 0 || options.AgentUID == uint32(os.Geteuid()) || options.Principal == "" || options.Values == nil {
		return errWire
	}
	listener, err := listenMetadata(options.Socket, options.GroupID)
	if err != nil {
		return err
	}
	defer listener.Close()
	if options.Ready != nil {
		if err := options.Ready(); err != nil {
			return err
		}
	}
	server := rpc.Server{
		Limits:         rpc.Limits{RequestBytes: privateio.MaxControlFrame, ResponseBytes: privateio.MaxControlFrame, Timeout: 30 * time.Second},
		MaxConnections: 16,
		Principals: identity.ResolverFunc(func(pid int32, uid, gid uint32) identity.Principal {
			kind := identity.Unknown
			if uid == options.AgentUID {
				kind = identity.Agent
			}
			return identity.Principal{Kind: kind, PID: pid, UID: uid, GID: gid}
		}),
		Operations: map[string]rpc.Operation{},
	}
	for _, operation := range []string{"inspect", "write", "delete"} {
		server.Operations[operation] = rpc.Operation{Grant: controlpolicy.Agent, Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
			if options.Serving != nil && !options.Serving() {
				return nil, fault.New(fault.CodeUnavailable, "application value roles are starting", true, "retry when the service is ready")
			}
			var request appvalues.RPCRequest
			if privateio.Decode(raw, &request) != nil || request.Operation != operation {
				return nil, fault.New(fault.CodeInvalidInput, "invalid application value request", false, "use the resource-scoped operation schema")
			}
			return options.Values.Handle(ctx, options.Principal, operation, request.Request)
		}}
	}
	return server.Serve(ctx, listener.UnixListener)
}

func listenMetadata(endpoint string, group uint32) (*Listener, error) {
	if !filepath.IsAbs(endpoint) || filepath.Clean(endpoint) != endpoint || len(endpoint) >= 108 {
		return nil, errWire
	}
	dir := filepath.Dir(endpoint)
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved != dir {
		return nil, errWire
	}
	parent, err := os.Lstat(dir)
	if err != nil || parent.Mode() != os.ModeDir|0750 {
		return nil, errWire
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errWire
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Uid != uint32(os.Geteuid()) || stat.Gid != group {
		return nil, errWire
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: endpoint, Net: "unix"})
	if err != nil {
		return nil, errWire
	}
	l.SetUnlinkOnClose(false)
	if os.Chown(endpoint, int(os.Geteuid()), int(group)) != nil || os.Chmod(endpoint, 0660) != nil {
		l.Close()
		return nil, errWire
	}
	socket, err := os.Lstat(endpoint)
	current, parentErr := os.Lstat(dir)
	if err != nil || parentErr != nil || !os.SameFile(parent, current) || socket.Mode() != os.ModeSocket|0660 {
		l.Close()
		return nil, errWire
	}
	return &Listener{UnixListener: l, path: endpoint, parent: parent, socket: socket}, nil
}
