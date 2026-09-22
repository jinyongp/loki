package network

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"loki/internal/platform/netguard"
)

func RunBrowserProxy(ctx context.Context, listener *net.TCPListener, policy netguard.Policy, ready func() error) error {
	proxy := netguard.New(policy)
	return runHTTPProxy(ctx, listener, proxy, proxy.Close, ready, false)
}
func runHTTPProxy(ctx context.Context, listener *net.TCPListener, handler http.Handler, closeProxy func(), ready func() error, allowUnspecified bool) error {
	defer closeProxy()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !address.IP.IsLoopback() && !(allowUnspecified && address.IP.IsUnspecified()) {
		return errors.New("proxy must listen on loopback or an explicitly allowed container address")
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 65536, BaseContext: func(net.Listener) context.Context { return ctx }}
	defer server.Close()
	if ready != nil {
		if err := ready(); err != nil {
			return err
		}
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		closeProxy()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	err := server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) && ctx.Err() != nil {
		return nil
	}
	return err
}
