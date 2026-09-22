package network

import (
	"context"
	"net"
	"time"

	"loki/internal/audit"
	"loki/internal/egress"
)

func RunEgressProxy(
	ctx context.Context, listener *net.TCPListener, policy egress.Policy, profile string,
	log *audit.Log, ready func() error, onAuditError func(error),
) error {
	return runEgressProxy(ctx, listener, policy, profile, "", log, ready, onAuditError)
}

func RunAuthenticatedEgressProxy(
	ctx context.Context, listener *net.TCPListener, policy egress.Policy, profile, authToken string,
	log *audit.Log, ready func() error, onAuditError func(error),
) error {
	return runEgressProxy(ctx, listener, policy, profile, authToken, log, ready, onAuditError)
}

func runEgressProxy(
	ctx context.Context, listener *net.TCPListener, policy egress.Policy, profile, authToken string,
	log *audit.Log, ready func() error, onAuditError func(error),
) error {
	proxy, err := egress.NewAuthenticated(policy, profile, authToken, func(decision egress.Decision) {
		if log == nil {
			return
		}
		err := log.Append(map[string]any{
			"timestamp": float64(time.Now().UnixMicro()) / 1e6,
			"operation": "egress-connect",
			"profile":   decision.Profile,
			"host":      decision.Host,
			"port":      decision.Port,
			"allowed":   decision.Allowed,
			"reason":    decision.Reason,
		})
		if err != nil && onAuditError != nil {
			onAuditError(err)
		}
	})
	if err != nil {
		return err
	}
	return runHTTPProxy(ctx, listener, proxy, proxy.Close, ready, true)
}
