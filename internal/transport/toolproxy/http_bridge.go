package toolproxy

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type HTTPBridgeOptions struct {
	Root, Origin, Version string
	Token                 []byte
	Command               func(context.Context) *exec.Cmd
	Authorize             func(string) error
	Diagnostics           io.Writer
}

func ServeHTTPBridge(ctx context.Context, options HTTPBridgeOptions) error {
	if !strings.HasPrefix(options.Origin, "http://127.0.0.1:") || len(options.Token) != 64 || options.Command == nil || options.Authorize == nil {
		return fmt.Errorf("invalid owned loopback bridge")
	}
	create := func(ctx context.Context) (*mcp.Server, *Group, func(), error) {
		command := options.Command(ctx)
		authorize := options.Authorize
		var group *Group
		server := mcp.NewServer(&mcp.Implementation{Name: "loki", Version: options.Version}, &mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{}, InitializedHandler: func(ctx context.Context, r *mcp.InitializedRequest) { group.SyncRoots(ctx, r.Session) }, RootsListChangedHandler: func(ctx context.Context, r *mcp.RootsListChangedRequest) { group.SyncRoots(ctx, r.Session) }})
		group, err := AttachMany(ctx, server, []Options{{Name: "loki", Owner: "full", Version: options.Version, Command: command, RootURI: "file:///workspace", Authorize: authorize, AuthorizeResource: func() error { return authorize("") }, ForwardOwnedResources: true, Stderr: options.Diagnostics}}, nil)
		if err != nil {
			return nil, nil, nil, err
		}
		return server, group, func() {}, nil
	}
	fallback := mcp.NewServer(&mcp.Implementation{Name: "loki", Version: options.Version}, nil)
	pool := NewHTTPPool(fallback, create)
	defer pool.Close()
	transport := pool.Handler(mcp.NewStreamableHTTPHandler(pool.Server, &mcp.StreamableHTTPOptions{JSONResponse: true, SessionTimeout: 20 * time.Minute, MaxRequestBodyBytes: 16 << 20}))
	origin := options.Origin
	address := origin[len("http://"):]
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		return fmt.Errorf("owned connection bridge cannot listen; an existing listener was retained: %w", err)
	}
	defer listener.Close()
	owner := sha256.Sum256([]byte(filepath.Clean(options.Root)))
	bridgeContext, stopBridge := context.WithCancel(ctx)
	defer stopBridge()
	server := &http.Server{ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != address {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/.well-known/oauth-protected-resource" || r.URL.Path == "/.well-known/oauth-protected-resource/mcp" {
			http.NotFound(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), append([]byte("Bearer "), options.Token...)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"owner": fmt.Sprintf("%x", owner), "pid": os.Getpid()})
			return
		}
		if r.URL.Path == "/shutdown" && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			stopBridge()
			return
		}
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		transport.ServeHTTP(w, r)
	})}
	context.AfterFunc(bridgeContext, func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	})
	err = server.Serve(listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
