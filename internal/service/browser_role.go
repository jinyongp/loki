package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"time"

	"loki/internal/browser"
	"loki/internal/control/identity"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/daemon"
	"loki/internal/fault"
	"loki/internal/rpc"
)

type BrowserCaller interface {
	Call(context.Context, string, map[string]any) (map[string]any, error)
}

func BrowserLimits() rpc.Limits {
	return rpc.Limits{RequestBytes: 1048576, ResponseBytes: 16777216, Timeout: 90 * time.Second}
}

type BrowserRPC struct{ Client rpc.Client }

func NewBrowserRPC(socket string, uid uint32) BrowserRPC {
	return BrowserRPC{Client: rpc.Client{Socket: socket, ExpectedUID: &uid, Limits: BrowserLimits()}}
}
func (c BrowserRPC) Call(ctx context.Context, operation string, args map[string]any) (map[string]any, error) {
	if _, err := os.Stat(c.Client.Socket); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fault.Error("browser is not configured")
		}
		return nil, err
	}
	if args == nil {
		args = map[string]any{}
	}
	var result map[string]any
	err := runtimeDecode(ctx, c.Client, map[string]any{"operation": operation, "arguments": args}, &result)
	var networkError *net.OpError
	if errors.As(err, &networkError) {
		return nil, fault.Error("browser is unavailable")
	}
	return result, err
}
func BrowserOperations(driver BrowserCaller) map[string]rpc.Operation {
	operations := map[string]rpc.Operation{}
	for _, operation := range []string{"start", "navigate", "state", "click", "hover", "drag", "wheel", "fill", "type", "key", "shortcut", "select_option", "set_checked", "focus", "upload", "dialog_state", "handle_dialog", "back", "forward", "reload", "stop_loading", "list_tabs", "switch_tab", "close_tab", "screenshot", "console", "network", "request", "websockets", "page_errors", "debug_diagnostics", "downloads", "stop"} {
		operations[operation] = rpc.Operation{Grant: controlpolicy.Agent, Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var envelope struct{ Arguments json.RawMessage }
			if json.Unmarshal(raw, &envelope) != nil {
				return nil, fault.Error("invalid browser request")
			}
			args := map[string]any{}
			if len(envelope.Arguments) > 0 {
				if envelope.Arguments[0] != '{' {
					return nil, fault.Error("browser arguments must be an object")
				}
				decoder := json.NewDecoder(bytes.NewReader(envelope.Arguments))
				decoder.UseNumber()
				if decoder.Decode(&args) != nil {
					return nil, fault.Error("invalid browser arguments")
				}
			}
			result, err := driver.Call(ctx, operation, args)
			if err != nil {
				return nil, fault.Error(err.Error())
			}
			return result, nil
		}}
	}
	return operations
}

type BrowserOptions struct {
	Socket    string
	AgentUID  uint32
	SocketGID int
	Browser   browser.Options
}

func RunBrowser(ctx context.Context, options BrowserOptions, ready func() error) error {
	if !filepath.IsAbs(options.Socket) || options.SocketGID < 0 {
		return fault.Error("browser requires an absolute socket path and socket GID")
	}
	driver, err := browser.NewDriver(options.Browser)
	if err != nil {
		return err
	}
	defer driver.Close()
	listener, err := daemon.Listen(options.Socket, options.SocketGID)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := rpc.Server{Principals: identity.UnixResolver{AgentUID: options.AgentUID}, Limits: BrowserLimits(), MaxConnections: 8, Operations: BrowserOperations(driver)}
	if ready != nil {
		if err = ready(); err != nil {
			return err
		}
	}
	return server.Serve(ctx, listener)
}
