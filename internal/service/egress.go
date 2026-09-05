package service

import (
	"context"
	"net"

	"loki/internal/egress"
)

func RunEgressProxy(ctx context.Context, listener *net.TCPListener, ready func() error) error {
	proxy := egress.New()
	return runLoopbackProxy(ctx, listener, proxy, proxy.Close, ready)
}
