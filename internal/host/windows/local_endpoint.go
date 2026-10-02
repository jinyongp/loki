package windows

import (
	"context"
	"fmt"
	"net"
	"time"

	"loki/internal/progress"
)

// LocalEndpointWaiter checks the Windows-side listener, which can become ready
// after the appliance's internal health checks during WSL startup.
type LocalEndpointWaiter struct {
	Dial       func(context.Context, string, string) (net.Conn, error)
	Sleep      SleepFunc
	Attempts   int
	RetryDelay time.Duration
	Progress   progress.Reporter
}

func (waiter LocalEndpointWaiter) Wait(ctx context.Context, endpoint string) error {
	_, port, err := parseLoopbackOrigin(endpoint, false)
	if err != nil {
		return err
	}
	dial := waiter.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: 2 * time.Second}).DialContext
	}
	attempts := waiter.Attempts
	if attempts <= 0 {
		attempts = 30
	}
	delay := waiter.RetryDelay
	if delay <= 0 {
		delay = 2 * time.Second
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		connection, dialErr := dial(ctx, "tcp4", fmt.Sprintf("127.0.0.1:%d", port))
		if dialErr == nil {
			_ = connection.Close()
			return nil
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if attempt == attempts {
			return newOpenAIDoctorError(endpoint, []string{"mcp_server_reachable"},
				[]string{fmt.Sprintf("local endpoint unavailable after %d attempts: %v", attempt, dialErr)},
				map[string]string{"mcp_server_reachable": dialErr.Error()})
		}
		if attempt == 1 {
			progress.Emit(waiter.Progress, progress.Event{
				Operation: "connection", Phase: "local-readiness", State: progress.StateStarted,
				Level: progress.LevelSummary, Message: "Waiting for the local Loki MCP endpoint to become reachable from Windows...",
			})
		}
		if waiter.Sleep != nil {
			err = waiter.Sleep(ctx, delay)
		} else {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				err = ctx.Err()
			case <-timer.C:
			}
			timer.Stop()
		}
		if err != nil {
			return err
		}
	}
	return nil
}
