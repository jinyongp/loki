package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"loki/internal/config"
)

// RunMCP owns the listener and waits for request cancellation before releasing
// the app's pinned roots, process sessions and temporary shares.
func RunMCP(ctx context.Context, c config.Config, options MCPOptions, listener *net.TCPListener, ready func() error) error {
	defer listener.Close()
	address := listener.Addr().(*net.TCPAddr)
	if !address.IP.IsLoopback() || !address.IP.Equal(net.ParseIP(c.Host)) || address.Port != c.Port {
		return errors.New("MCP listener does not match configured loopback address")
	}
	app, err := NewMCP(c, options)
	if err != nil {
		return err
	}
	defer app.Close()
	server := &http.Server{Handler: app, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 65536, BaseContext: func(net.Listener) context.Context { return ctx }}
	defer server.Close()
	if ready != nil {
		if err = ready(); err != nil {
			return err
		}
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if server.Shutdown(shutdown) != nil {
			server.Close()
		}
	})
	defer func() {
		if !stop() {
			<-done
		}
	}()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) && ctx.Err() != nil {
		return nil
	}
	return err
}
