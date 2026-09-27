package windows

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type ConnectionStartupConnections interface {
	ValidateEnabled(context.Context, string) (int, error)
	ReconcileEnabled(context.Context, string) error
}

type ConnectionStartupPlatform interface {
	VerifyCanonicalFrontend(context.Context) (FrontendPaths, error)
	Collect(context.Context, ExpectedInstallation) (ExistingSnapshot, error)
	StartKeepalive(context.Context, ExpectedInstallation) error
	Doctor(context.Context, string) (NativeProbe, error)
	Sleep(context.Context, time.Duration) error
}

type ConnectionStartupResult struct {
	EnabledConnections int
	KeepaliveStarted   bool
	HealthAttempts     int
}

type ConnectionStartupController struct {
	Platform    ConnectionStartupPlatform
	Connections ConnectionStartupConnections
	Attempts    int
	RetryDelay  time.Duration
}

func (controller ConnectionStartupController) Run(
	ctx context.Context,
	expected ExpectedInstallation,
) (ConnectionStartupResult, error) {
	if controller.Platform == nil || controller.Connections == nil {
		return ConnectionStartupResult{}, errors.New("managed connection startup controller is incomplete")
	}
	if _, err := controller.Platform.VerifyCanonicalFrontend(ctx); err != nil {
		return ConnectionStartupResult{}, fmt.Errorf("verify canonical Windows frontend before connection startup: %w", err)
	}
	enabled, err := controller.Connections.ValidateEnabled(ctx, expected.Distribution)
	if err != nil {
		return ConnectionStartupResult{}, fmt.Errorf("verify enabled managed connection ownership: %w", err)
	}
	result := ConnectionStartupResult{EnabledConnections: enabled}
	if enabled == 0 {
		return result, nil
	}

	snapshot, err := controller.Platform.Collect(ctx, expected)
	if err != nil {
		return result, fmt.Errorf("inspect Loki appliance before connection startup: %w", err)
	}
	if !snapshot.Windows.Present || !snapshot.Windows.Owned {
		return result, errors.New("refusing connection startup without verified Windows appliance ownership")
	}
	switch snapshot.Distribution.State {
	case DistributionHealthy:
		result.HealthAttempts = 1
	case DistributionStale:
		if !snapshot.StartupTask.Present || !snapshot.StartupTask.Owned {
			return result, errors.New("Loki appliance is not healthy and the verified WSL keepalive task is unavailable")
		}
		if err = controller.Platform.StartKeepalive(ctx, expected); err != nil {
			return result, fmt.Errorf("start verified Loki WSL keepalive task: %w", err)
		}
		result.KeepaliveStarted = true
		attempts := controller.Attempts
		if attempts <= 0 {
			attempts = 30
		}
		delay := controller.RetryDelay
		if delay <= 0 {
			delay = 2 * time.Second
		}
		healthy := false
		for attempt := 1; attempt <= attempts; attempt++ {
			probe, probeErr := controller.Platform.Doctor(ctx, expected.Distribution)
			result.HealthAttempts = attempt
			if probeErr != nil {
				return result, fmt.Errorf("probe Loki appliance health after keepalive start: %w", probeErr)
			}
			if probe.ExitCode == 0 {
				healthy = true
				break
			}
			if attempt == attempts {
				break
			}
			if err = controller.Platform.Sleep(ctx, delay); err != nil {
				return result, err
			}
		}
		if !healthy {
			return result, fmt.Errorf("Loki appliance did not become healthy after %d bounded startup attempts", result.HealthAttempts)
		}
	case DistributionAbsent:
		return result, errors.New("Loki appliance distribution is absent")
	case DistributionProvisioning:
		return result, errors.New("Loki appliance provisioning is still in progress")
	case DistributionForeign, DistributionIndeterminate:
		return result, errors.New("Loki appliance ownership or health is indeterminate")
	default:
		return result, fmt.Errorf("unsupported Loki appliance state %q", snapshot.Distribution.State)
	}

	if err = controller.Connections.ReconcileEnabled(ctx, expected.Distribution); err != nil {
		return result, fmt.Errorf("restore enabled managed connections: %w", err)
	}
	return result, nil
}
