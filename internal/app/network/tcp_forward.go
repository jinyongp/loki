package network

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// RunTCPForward relays TCP connections without terminating or inspecting the
// application protocol. The caller owns the listener and fixes the target.
func RunTCPForward(ctx context.Context, listener *net.TCPListener, target string) error {
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !address.IP.IsLoopback() && !address.IP.IsUnspecified() {
		return errors.New("TCP forward must listen on loopback or a container address")
	}
	if host, port, err := net.SplitHostPort(target); err != nil || host == "" || port == "" {
		return errors.New("TCP forward target must be a host and port")
	}

	var connections sync.WaitGroup
	defer connections.Wait()
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()

	for {
		client, err := listener.AcceptTCP()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		connections.Add(1)
		go func() {
			defer connections.Done()
			defer client.Close()
			upstream, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", target)
			if err != nil {
				return
			}
			defer upstream.Close()
			stopConnection := context.AfterFunc(ctx, func() {
				_ = client.Close()
				_ = upstream.Close()
			})
			defer stopConnection()

			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(upstream, client); done <- struct{}{} }()
			go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
			<-done
		}()
	}
}
