package browserapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/control/identity"
	"loki/internal/daemon"
	"loki/internal/rpc"
	"loki/internal/transport/toolproxy"
	projectbrowser "loki/modules/browser"
)

// OfficialOptions is a protected browser-role layout. The service process
// already runs as the browser identity under the execution contract's network
// policy; it must receive neither provider keys nor application secret state.
type OfficialOptions struct {
	Socket, Bundle, Data, Workspace, Proxy string
	AgentUID                               uint32
	SocketGID                              int
	Capabilities                           []string
	Stderr                                 io.Writer
}

func RunOfficialBrowser(ctx context.Context, o OfficialOptions, ready func() error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if os.Getuid() == 0 || o.AgentUID == 0 || uint32(os.Getuid()) == o.AgentUID {
		return errors.New("official full-mode browser requires its separate non-root service identity")
	}
	for _, path := range []string{o.Socket, o.Bundle, o.Data, o.Workspace} {
		if !filepath.IsAbs(path) {
			return errors.New("official browser role paths must be absolute")
		}
	}
	if o.Proxy == "" || o.SocketGID < 0 {
		return errors.New("official browser requires its execution-contract proxy and socket group")
	}
	if err := projectbrowser.ValidateCapabilitiesAndProxy(o.Capabilities, o.Proxy); err != nil {
		return err
	}
	relativeWorkspace, err := filepath.Rel(o.Data, o.Workspace)
	if err != nil || !filepath.IsLocal(relativeWorkspace) || relativeWorkspace == "." {
		return errors.New("official browser workspace must be a private child of its owned data directory")
	}
	if err := daemon.PrivateDirectory(o.Data); err != nil {
		return err
	}
	if err := daemon.PrivateDirectory(o.Workspace); err != nil {
		return err
	}
	realData, err := filepath.EvalSymlinks(o.Data)
	if err != nil {
		return err
	}
	realWorkspace, err := filepath.EvalSymlinks(o.Workspace)
	if err != nil {
		return err
	}
	relativeWorkspace, err = filepath.Rel(realData, realWorkspace)
	if err != nil || !filepath.IsLocal(relativeWorkspace) || relativeWorkspace == "." {
		return errors.New("official browser workspace escapes its owned data directory")
	}
	if err := projectbrowser.CheckRuntime(ctx, o.Bundle); err != nil {
		return err
	}
	listener, err := daemon.Listen(o.Socket, o.SocketGID)
	if err != nil {
		return err
	}
	defer listener.Close()
	stopListener := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stopListener()
	if ready != nil {
		if err := ready(); err != nil {
			return err
		}
	}
	var active sync.WaitGroup
	defer func() { cancel(); active.Wait() }()
	capacity := make(chan struct{}, 8)
	for {
		connection, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		peer, err := rpc.PeerCredentials(connection)
		if err != nil || (identity.UnixResolver{AgentUID: o.AgentUID}).Resolve(peer.PID, peer.UID, peer.GID).Kind != identity.Agent {
			_ = connection.Close()
			continue
		}
		select {
		case capacity <- struct{}{}:
		default:
			_ = connection.Close()
			continue
		}
		active.Add(1)
		go func() {
			defer active.Done()
			defer func() { <-capacity }()
			defer connection.Close()
			stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
			defer stop()
			if err := serveOfficialPeer(ctx, o, connection); err != nil && ctx.Err() == nil && o.Stderr != nil {
				fmt.Fprintln(o.Stderr, "Official browser session failed:", err)
			}
		}()
	}
}

func serveOfficialPeer(ctx context.Context, o OfficialOptions, connection *net.UnixConn) error {
	options := make([]toolproxy.Options, 0, 2)
	for _, name := range []string{"playwright", "devtools"} {
		launch, err := projectbrowser.Prepare(ctx, projectbrowser.Options{Bundle: o.Bundle, Data: o.Data, Workspace: o.Workspace, Engine: name, Proxy: o.Proxy, RuntimeVerified: true, Capabilities: o.Capabilities, Stderr: o.Stderr})
		if err != nil {
			return err
		}
		defer launch.Cleanup()
		options = append(options, toolproxy.Options{
			Name: "loki-full-browser", Owner: "browser/" + name, Version: "0.2.0", Command: launch.Command,
			Instructions: "Official browser engines in the protected Loki browser service. Each engine has separate tabs, profiles and login state. Use loki_browser_files for owned screenshots and upload staging.",
			RootURI:      launch.RootURI, Results: launch.Output, Stderr: o.Stderr,
			Authorize: func(tool string) error {
				if !projectbrowser.Allowed(tool, o.Capabilities) {
					return errors.New("browser tool requires an explicit unsafe-code capability")
				}
				return nil
			},
			AuthorizeResource: func() error { return nil },
		})
	}
	return toolproxy.RunManyTransport(ctx, options, &mcp.IOTransport{Reader: connection, Writer: connection, MaxLineLength: 16 << 20})
}
