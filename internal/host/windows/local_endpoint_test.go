package windows

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestLocalEndpointWaiterWaitsForWindowsListener(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	attempts, sleeps := 0, 0
	waiter := LocalEndpointWaiter{
		Attempts: 3,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			attempts++
			if attempts < 3 {
				return nil, errors.New("connection refused")
			}
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
		Sleep: func(context.Context, time.Duration) error { sleeps++; return nil },
	}
	if err := waiter.Wait(t.Context(), "http://"+listener.Addr().String()+"/mcp"); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || sleeps != 2 {
		t.Fatalf("attempts=%d sleeps=%d", attempts, sleeps)
	}
}

func TestLocalEndpointWaiterBoundsFailureAndRejectsNonlocalURLs(t *testing.T) {
	attempts, sleeps := 0, 0
	waiter := LocalEndpointWaiter{
		Attempts: 3,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			attempts++
			return nil, errors.New("connection refused")
		},
		Sleep: func(context.Context, time.Duration) error { sleeps++; return nil },
	}
	err := waiter.Wait(t.Context(), "http://127.0.0.1:18765/mcp")
	var failure *OpenAIDoctorError
	if !errors.As(err, &failure) || !strings.Contains(err.Error(), "connection refused") || attempts != 3 || sleeps != 2 {
		t.Fatalf("err=%v attempts=%d sleeps=%d", err, attempts, sleeps)
	}
	if err := waiter.Wait(t.Context(), "http://example.invalid:18765/mcp"); err == nil || attempts != 3 {
		t.Fatal("nonlocal endpoint was contacted")
	}
}

func TestLocalEndpointWaiterHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	waiter := LocalEndpointWaiter{Dial: func(context.Context, string, string) (net.Conn, error) {
		calls++
		cancel()
		return nil, errors.New("connection refused")
	}}
	if err := waiter.Wait(ctx, "http://127.0.0.1:18765/mcp"); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestOpenAIWaitsBeforeAnyTunnelMutation(t *testing.T) {
	for _, action := range []string{"setup", "start"} {
		t.Run(action, func(t *testing.T) {
			adapter, runtime, credentials, runner, local, store := openAIAdapterFixture()
			if err := adapter.Setup(t.Context(), runtime); err != nil {
				t.Fatal(err)
			}
			runner.calls = nil
			waitErr := errors.New("local endpoint unavailable")
			adapter.WaitLocalEndpoint = func(_ context.Context, endpoint string) error {
				if endpoint != local.material.LocalOrigin {
					t.Fatalf("wait used stale endpoint %q", endpoint)
				}
				return waitErr
			}
			writes, puts := store.writes, len(credentials.puts)
			var err error
			if action == "setup" {
				err = adapter.Setup(t.Context(), runtime)
			} else {
				err = adapter.Start(t.Context(), runtime)
			}
			if !errors.Is(err, waitErr) || len(runner.calls) != 0 || store.writes != writes || len(credentials.puts) != puts {
				t.Fatalf("tunnel changed before readiness: err=%v helper=%v", err, runner.calls)
			}
		})
	}
}
