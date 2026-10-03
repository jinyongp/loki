package management

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"
)

func (b *DockerFullBackend) Start(ctx context.Context, r DeploymentReservation) error {
	record, err := b.record(r)
	if err != nil {
		return err
	}
	for _, name := range record.Order {
		var spec fullContainerSpec
		for _, current := range record.Specs {
			if current.Service == name {
				spec = current
				break
			}
		}
		inspection, err := b.inspectContainer(ctx, spec.Name)
		if err != nil {
			return err
		}
		if err := b.verifySpec(r, spec, inspection); err != nil {
			return err
		}
		if !inspection.State.Running {
			fmt.Fprintf(b.Diagnostics, "Starting %s...\n", name)
			if _, err := b.command(ctx, "starting "+name, nil, "container", "start", spec.Name); err != nil {
				return err
			}
		}
		// Dependencies start in order. Configuration readiness is observed
		// after all roles start, so an unconfigured provider can be set up.
		if err := b.waitService(ctx, record, spec); err != nil {
			return err
		}
	}
	return nil
}

func (b *DockerFullBackend) waitService(ctx context.Context, record fullDockerRecord, spec fullContainerSpec) error {
	fmt.Fprintf(b.Diagnostics, "Waiting for %s readiness...\n", spec.Service)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var last error
	for {
		last = b.probeService(ctx, record, spec, "")
		if last == nil {
			return nil
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%s has not become ready: %w", spec.Service, last)
		case <-timer.C:
		}
	}
}

func (b *DockerFullBackend) probeService(ctx context.Context, record fullDockerRecord, spec fullContainerSpec, module string) error {
	inspection, err := b.inspectContainer(ctx, spec.Name)
	if err != nil {
		return err
	}
	if err := b.verifySpec(record.Reservation, spec, inspection); err != nil {
		return err
	}
	if !inspection.State.Running {
		return fmt.Errorf("%s is %s", spec.Service, inspection.State.Status)
	}
	role, user := spec.Service, "10000:10001"
	args := []string{}
	switch role {
	case "endpoints":
		args = []string{"--socket", "/run/loki/endpoints/control.sock", "--expected-uid", "0"}
	case "runtime":
		args = []string{"--socket", "/run/loki/runtime/control.sock", "--expected-uid", "0"}
		if module != "" {
			args = append(args, "--module", module)
		}
	case "launcher":
		user = "10004:10001"
		args = []string{"--socket", "/run/loki/launcher/control.sock", "--expected-uid", "0"}
	case "executor":
		args = []string{"--socket", "/run/loki/executor/control.sock", "--expected-uid", "10004"}
	case "browser":
		args = []string{"--socket", "/run/loki/browser/control.sock", "--expected-uid", "10003"}
	case "git-signing":
		role = "signing"
		args = []string{"--socket", "/run/loki/signing/agent.sock", "--expected-uid", "0"}
	case "mcp":
		args = []string{"--address", "http://127.0.0.1:18765/mcp", "--token-file", "/etc/loki/auth/token"}
		if module == "git" || module == "workspace" {
			role = "module"
			args = []string{"--module", module}
		}
	case "egress", "browser-proxy":
		port := 18766
		if role == "browser-proxy" {
			port = 18767
		}
		role = "proxy"
		user = spec.User
		args = []string{"--address", "http://127.0.0.1:" + strconv.Itoa(port) + "/"}
	default:
		return fmt.Errorf("unknown service readiness probe")
	}
	command := []string{"container", "exec", "--user", user, spec.Name, record.Worker, "full-probe", "--role", role}
	command = append(command, args...)
	_, err = b.command(ctx, "probing "+spec.Service, nil, command...)
	return err
}

func (b *DockerFullBackend) Observe(ctx context.Context, r DeploymentReservation) (FullObservation, error) {
	result := FullObservation{State: "stopped", Ready: false, Services: map[string]string{}}
	record, err := b.record(r)
	if err != nil {
		return result, err
	}
	result.State, result.Ready = "running", true
	for _, spec := range record.Specs {
		if err := b.probeService(ctx, record, spec, ""); err != nil {
			result.Services[spec.Service] = err.Error()
			result.Ready = false
			result.State = "degraded"
		} else {
			result.Services[spec.Service] = "ready"
		}
	}
	// Provider credentials and module-native executables are distinct from a
	// running service. Probe each selected implementation's actual dependency.
	state, err := b.Store.Load()
	if err != nil {
		return result, err
	}
	if slices.Contains(r.Services, "mcp") {
		for _, choice := range state.Config.Tools {
			if !choice.Enabled || !slices.Contains([]string{"github", "git", "workspace"}, string(choice.ID)) {
				continue
			}
			service := "mcp"
			if choice.ID == "github" {
				service = "runtime"
			}
			for _, spec := range record.Specs {
				if spec.Service == service {
					if err := b.probeService(ctx, record, spec, string(choice.ID)); err != nil {
						result.Services[string(choice.ID)] = err.Error()
						result.Ready = false
						result.State = "degraded"
					} else {
						result.Services[string(choice.ID)] = "ready"
					}
				}
			}
		}
	}
	return result, nil
}

var _ FullBackend = (*DockerFullBackend)(nil)
