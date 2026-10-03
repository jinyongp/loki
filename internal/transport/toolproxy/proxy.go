// Package toolproxy forwards official engine tool schemas and results over MCP.
package toolproxy

import (
	"context"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

type Options struct {
	Name         string
	Version      string
	Instructions string
	// Owner is a unique composition binding owner, for example browser/playwright.
	Owner                 string
	Command               *exec.Cmd
	Transport             mcp.Transport
	RootURI               string
	Authorize             func(string) error
	Stderr                io.Writer
	Revision              func() string
	Results               string
	AuthorizeResource     func() error
	ForwardOwnedResources bool
}

type progressTarget struct {
	session *mcp.ServerSession
	token   any
}

// Run serves one upstream engine per stdio session. Context cancellation closes
// the owned command transport; no existing desktop browser is attached.
func Run(ctx context.Context, o Options) error {
	return RunMany(ctx, []Options{o})
}

// RunMany composes independently owned official engines on one MCP server.
// Public names retain their native schemas and must be unique across engines.
func RunMany(ctx context.Context, options []Options) error {
	return RunManyTransport(ctx, options, &mcp.StdioTransport{})
}

// RunManyTransport serves the same official engine implementation through an
// explicitly supplied transport, including a verified protected service peer.
func RunManyTransport(ctx context.Context, options []Options, transport mcp.Transport) error {
	if len(options) == 0 {
		return fmt.Errorf("proxy requires at least one engine")
	}
	var group *Group
	o := options[0]
	server := mcp.NewServer(&mcp.Implementation{Name: o.Name, Version: o.Version}, &mcp.ServerOptions{
		Instructions:            o.Instructions,
		Capabilities:            &mcp.ServerCapabilities{},
		InitializedHandler:      func(ctx context.Context, req *mcp.InitializedRequest) { group.SyncRoots(ctx, req.Session) },
		RootsListChangedHandler: func(ctx context.Context, req *mcp.RootsListChangedRequest) { group.SyncRoots(ctx, req.Session) },
	})
	var err error
	group, err = AttachMany(ctx, server, options, nil)
	if err != nil {
		return err
	}
	defer group.Close()
	return server.Run(ctx, transport)
}

// Group owns upstream sessions attached to an already assembled MCP server.
// The host delegates its initialization/root notifications to SyncRoots.
type Group struct {
	mu         sync.Mutex
	engines    []*engineSession
	closeFiles func()
	once       sync.Once
}

func (g *Group) SyncRoots(ctx context.Context, session *mcp.ServerSession) {
	if g == nil {
		return
	}
	g.mu.Lock()
	current := append([]*engineSession(nil), g.engines...)
	g.mu.Unlock()
	for _, engine := range current {
		engine.syncRoots(ctx, session)
	}
}

func (g *Group) Close() {
	if g == nil {
		return
	}
	g.once.Do(func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		for i := len(g.engines) - 1; i >= 0; i-- {
			g.engines[i].close()
		}
		if g.closeFiles != nil {
			g.closeFiles()
		}
	})
}

// AttachMany reserves the assembled server's static names before actual engine
// discovery. Engine changes can never overwrite one of those implementations.
func AttachMany(ctx context.Context, server *mcp.Server, options []Options, staticNames []string) (group *Group, err error) {
	group = &Group{}
	owned := group
	defer func() {
		if err != nil {
			owned.Close()
		}
	}()
	owners := &bindingOwners{}
	if err = owners.replace("loki/static", staticNames, func() {}); err != nil {
		return nil, err
	}
	group.closeFiles, err = registerFiles(ctx, server, owners, options)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, option := range options {
		if option.Owner == "" {
			option.Owner = option.Name
		}
		if option.Owner == "" || option.Owner == "loki/browser-files" || option.Owner == "loki/static" || seen[option.Owner] {
			return nil, fmt.Errorf("engine composition requires unique nonempty binding owners")
		}
		seen[option.Owner] = true
		engine, err := attach(ctx, server, owners, option)
		if err != nil {
			return nil, err
		}
		group.engines = append(group.engines, engine)
	}
	return group, nil
}

type engineSession struct {
	syncRoots func(context.Context, *mcp.ServerSession)
	close     func()
}

func attach(ctx context.Context, server *mcp.Server, owners *bindingOwners, o Options) (*engineSession, error) {
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	if o.Authorize == nil || (o.Command == nil) == (o.Transport == nil) {
		return nil, fmt.Errorf("proxy requires exactly one command or transport and a call authorizer")
	}
	var client *mcp.Client
	var upstream *mcp.ClientSession
	resources := &ownedResourceProxy{server: server, owners: owners, options: o}
	if o.ForwardOwnedResources && o.AuthorizeResource == nil {
		return nil, fmt.Errorf("owned resource forwarding requires a resource authorizer")
	}
	var rootsMu sync.Mutex
	roots := []string{o.RootURI}
	if o.Results != "" {
		roots = append(roots, fileRootURI(o.Results))
	}
	syncRoots := func(ctx context.Context, session *mcp.ServerSession) {
		result, err := session.ListRoots(ctx, nil)
		if err != nil {
			return
		}
		rootsMu.Lock()
		defer rootsMu.Unlock()
		client.RemoveRoots(roots...)
		roots = nil
		for _, root := range result.Roots {
			roots = append(roots, root.URI)
		}
		client.AddRoots(result.Roots...)
		if o.Results != "" {
			ownedURI := fileRootURI(o.Results)
			roots = append(roots, ownedURI)
			client.AddRoots(&mcp.Root{URI: ownedURI, Name: "Owned browser session files"})
		}
	}
	var progressMu sync.Mutex
	progress := map[string]progressTarget{}
	var serial atomic.Uint64
	var refreshMu sync.Mutex
	var closed bool
	var previous []string
	var refresh func(context.Context, *mcp.ClientSession) error
	client = mcp.NewClient(&mcp.Implementation{Name: o.Name + "-engine", Version: o.Version}, &mcp.ClientOptions{
		ResourceListChangedHandler: func(ctx context.Context, request *mcp.ResourceListChangedRequest) {
			if o.ForwardOwnedResources {
				if err := resources.refresh(ctx, request.Session); err != nil {
					fmt.Fprintln(o.Stderr, "Protected browser resource refresh failed:", err)
				}
			}
		},
		ToolListChangedHandler: func(ctx context.Context, req *mcp.ToolListChangedRequest) {
			if err := refresh(ctx, req.Session); err != nil {
				fmt.Fprintf(o.Stderr, "Engine discovery update failed: %v\n", err)
			}
		},
		ProgressNotificationHandler: func(ctx context.Context, req *mcp.ProgressNotificationClientRequest) {
			token, ok := req.Params.ProgressToken.(string)
			if !ok {
				return
			}
			progressMu.Lock()
			target, found := progress[token]
			progressMu.Unlock()
			if found {
				params := *req.Params
				params.ProgressToken = target.token
				_ = target.session.NotifyProgress(ctx, &params)
			}
		},
	})
	client.AddRoots(&mcp.Root{URI: o.RootURI, Name: "Project workspace"})
	if o.Results != "" {
		client.AddRoots(&mcp.Root{URI: fileRootURI(o.Results), Name: "Owned browser session files"})
	}
	refresh = func(ctx context.Context, engine *mcp.ClientSession) error {
		refreshMu.Lock()
		defer refreshMu.Unlock()
		if closed {
			return nil
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		var definitions []*mcp.Tool
		for tool, err := range engine.Tools(ctx, nil) {
			if err != nil {
				_ = owners.replace(o.Owner, nil, func() { server.RemoveTools(previous...); previous = nil })
				return err
			}
			if len(definitions) >= 4096 {
				_ = owners.replace(o.Owner, nil, func() { server.RemoveTools(previous...); previous = nil })
				return fmt.Errorf("engine discovery exceeds the tool count limit")
			}
			definitions = append(definitions, tool)
		}
		var names []string
		var authorized []*mcp.Tool
		for _, definition := range definitions {
			if o.Authorize(definition.Name) == nil {
				names = append(names, definition.Name)
				authorized = append(authorized, definition)
			}
		}
		return owners.replace(o.Owner, names, func() {
			server.RemoveTools(previous...)
			previous = nil
			for _, definition := range authorized {
				definition := *definition
				name := definition.Name
				server.AddTool(&definition, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					if err := o.Authorize(name); err != nil {
						return nil, err
					}
					params := &mcp.CallToolParams{Name: name, Arguments: req.Params.Arguments, Meta: req.Params.Meta, InputResponses: req.Params.InputResponses, RequestState: req.Params.RequestState}
					if original := req.Params.GetProgressToken(); original != nil {
						token := fmt.Sprintf("%s-progress-%d", o.Owner, serial.Add(1))
						params.Meta = make(mcp.Meta, len(req.Params.Meta)+1)
						for k, v := range req.Params.Meta {
							params.Meta[k] = v
						}
						params.SetProgressToken(token)
						progressMu.Lock()
						progress[token] = progressTarget{req.Session, original}
						progressMu.Unlock()
						defer func() { progressMu.Lock(); delete(progress, token); progressMu.Unlock() }()
					}
					callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
					defer cancel()
					result, err := engine.CallTool(callCtx, params)
					if callCtx.Err() != nil {
						_ = engine.Close()
						return nil, fmt.Errorf("browser call canceled or timed out; owned session stopped, reconnect to continue: %w", callCtx.Err())
					}
					return result, err
				})
				previous = append(previous, name)
			}
		})
	}
	fmt.Fprintf(o.Stderr, "Starting %s official engine...\n", o.Name)
	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	var err error
	transport := o.Transport
	if transport == nil {
		transport = ownedTransport{command: o.Command}
	}
	upstream, err = client.Connect(startCtx, transport, nil)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("official engine initialization: %w", err)
	}
	if err := refresh(ctx, upstream); err != nil {
		_ = upstream.Close()
		return nil, fmt.Errorf("official engine discovery: %w", err)
	}
	if o.ForwardOwnedResources {
		if err := resources.refresh(ctx, upstream); err != nil {
			_ = upstream.Close()
			resources.close()
			return nil, fmt.Errorf("protected browser resource discovery: %w", err)
		}
	}
	watchCtx, stopWatch := context.WithCancel(ctx)
	if o.Revision != nil {
		go func() {
			last := o.Revision()
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-watchCtx.Done():
					return
				case <-ticker.C:
					current := o.Revision()
					if current == last {
						continue
					}
					last = current
					if err := refresh(watchCtx, upstream); err != nil {
						fmt.Fprintf(o.Stderr, "Browser discovery refresh failed: %v\n", err)
					}
					if o.ForwardOwnedResources {
						if err := resources.refresh(watchCtx, upstream); err != nil {
							fmt.Fprintf(o.Stderr, "Protected browser resource refresh failed: %v\n", err)
						}
					}
				}
			}
		}()
	}
	fmt.Fprintf(o.Stderr, "%s MCP connected; project-host session has its own tabs and login state.\n", o.Name)
	return &engineSession{syncRoots: syncRoots, close: func() {
		stopWatch()
		_ = upstream.Close()
		if o.ForwardOwnedResources {
			resources.close()
		}
		refreshMu.Lock()
		defer refreshMu.Unlock()
		closed = true
		_ = owners.replace(o.Owner, nil, func() { server.RemoveTools(previous...); previous = nil })
	}}, nil
}
