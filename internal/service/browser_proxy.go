package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"loki/internal/browsernet"
)

func RunBrowserProxy(ctx context.Context, listener *net.TCPListener, policy browsernet.Policy, ready func() error) error {
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !address.IP.Equal(net.ParseIP("127.0.0.1")) {
		return errors.New("browser proxy must listen on 127.0.0.1")
	}
	proxy := browsernet.New(policy)
	defer proxy.Close()
	server := &http.Server{Handler: proxy, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 65536, BaseContext: func(net.Listener) context.Context { return ctx }}
	defer server.Close()
	if ready != nil {
		if err := ready(); err != nil {
			return err
		}
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		proxy.Close()
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
