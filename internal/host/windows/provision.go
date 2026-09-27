package windows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type ProcessStarter interface {
	Start(string, []string) error
}

type ExecProcessStarter struct{}

func (ExecProcessStarter) Start(executable string, arguments []string) error {
	command := exec.Command(executable, arguments...)
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

type SleepFunc func(context.Context, time.Duration) error

type WSLFreshProvisioner struct {
	Client   WSLClient
	Starter  ProcessStarter
	Sleep    SleepFunc
	Attempts int
}

func (provisioner WSLFreshProvisioner) RegisterDistribution(
	ctx context.Context,
	expected ExpectedInstallation,
	options InstallOptions,
	appliance PreparedAppliance,
) (bool, error) {
	arguments := []string{"--install", "--from-file", appliance.Path, "--name", expected.Distribution, "--no-launch"}
	if options.InstallLocation != "" {
		arguments = append(arguments, "--location", options.InstallLocation)
	}
	result, err := provisioner.Client.run(ctx, arguments...)
	if err != nil {
		return false, err
	}
	if result.ExitCode != 0 {
		// A failed import does not prove that any same-name distribution was
		// created by this invocation. Never authorize rollback by name alone.
		return false, nativeFailure("register WSL distribution", result)
	}
	return true, nil
}

func (provisioner WSLFreshProvisioner) Provision(
	ctx context.Context,
	expected ExpectedInstallation,
	options InstallOptions,
) (ConnectionMaterial, error) {
	starter := provisioner.Starter
	if starter == nil {
		starter = ExecProcessStarter{}
	}
	if err := starter.Start(provisioner.Client.executable(),
		[]string{"-d", expected.Distribution, "--exec", "/usr/bin/sleep", "infinity"}); err != nil {
		return ConnectionMaterial{}, fmt.Errorf("start WSL keepalive: %w", err)
	}
	configure, err := provisioner.Client.run(ctx,
		"-d", expected.Distribution, "--user", "root", "--exec",
		"/usr/lib/loki-appliance/configure-install", strconv.Itoa(options.MCPPort))
	if err != nil {
		return ConnectionMaterial{}, err
	}
	if configure.ExitCode != 0 {
		return ConnectionMaterial{}, nativeFailure("configure Loki appliance install", configure)
	}

	attempts := provisioner.Attempts
	if attempts <= 0 {
		attempts = 300
	}
	sleep := provisioner.Sleep
	if sleep == nil {
		sleep = func(ctx context.Context, duration time.Duration) error {
			timer := time.NewTimer(duration)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}
	}
	ready := false
	for attempt := 0; attempt < attempts; attempt++ {
		service, runErr := provisioner.Client.run(ctx,
			"-d", expected.Distribution, "--exec", "/usr/bin/systemctl", "show",
			"loki-appliance-provision.service", "--no-pager",
			"--property=ActiveState", "--property=SubState", "--property=Result",
			"--property=NRestarts", "--property=ExecMainStatus")
		if runErr != nil {
			return ConnectionMaterial{}, runErr
		}
		if service.ExitCode == 0 {
			values := parseSystemdProperties(service.Stdout)
			active := values["ActiveState"]
			restarts, _ := strconv.Atoi(values["NRestarts"])
			if active == "active" {
				ready = true
				break
			}
			if active == "failed" || restarts >= 3 {
				return ConnectionMaterial{}, fmt.Errorf(
					"Loki appliance provisioning failed repeatedly (state=%s/%s, result=%s, exit=%s, restarts=%d)",
					active, values["SubState"], values["Result"], values["ExecMainStatus"], restarts,
				)
			}
		}
		if err := sleep(ctx, 2*time.Second); err != nil {
			return ConnectionMaterial{}, err
		}
	}
	if !ready {
		return ConnectionMaterial{}, errors.New("Loki appliance provisioning did not complete within the allowed attempts")
	}

	status, err := provisioner.Client.run(ctx, "-d", expected.Distribution, "--user", "root", "--exec",
		"/usr/local/bin/loki", "host", "status", "--system", "--json")
	if err != nil {
		return ConnectionMaterial{}, err
	}
	if status.ExitCode != 0 {
		return ConnectionMaterial{}, nativeFailure("verify Loki host status", status)
	}
	doctor, err := provisioner.Client.run(ctx, "-d", expected.Distribution, "--user", "root", "--exec",
		"/usr/local/bin/loki", "host", "doctor", "--system")
	if err != nil {
		return ConnectionMaterial{}, err
	}
	if doctor.ExitCode != 0 {
		return ConnectionMaterial{}, nativeFailure("verify Loki host doctor", doctor)
	}
	connectionResult, err := provisioner.Client.run(ctx, "-d", expected.Distribution, "--user", "root", "--exec",
		"/usr/local/bin/loki", "host", "connection", "--system", "--json")
	if err != nil {
		return ConnectionMaterial{}, err
	}
	if connectionResult.ExitCode != 0 {
		return ConnectionMaterial{}, nativeFailure("read Loki connection information", connectionResult)
	}
	tokenResult, err := provisioner.Client.run(ctx, "-d", expected.Distribution, "--user", "root", "--exec",
		"/bin/cat", "/var/lib/loki/lifecycle/mcp-token")
	if err != nil {
		return ConnectionMaterial{}, err
	}
	if tokenResult.ExitCode != 0 {
		return ConnectionMaterial{}, nativeFailure("read Loki MCP token", tokenResult)
	}
	token := strings.TrimSpace(tokenResult.Stdout)
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return ConnectionMaterial{}, errors.New("Loki MCP token material is invalid")
	}

	var connection struct {
		SchemaVersion int `json:"schema_version"`
		LocalOrigin   struct {
			URL            string `json:"url"`
			Transport      string `json:"transport"`
			Reachability   string `json:"reachability"`
			Authentication struct {
				Type string `json:"type"`
			} `json:"authentication"`
		} `json:"local_origin"`
	}
	if err := json.Unmarshal([]byte(connectionResult.Stdout), &connection); err != nil {
		return ConnectionMaterial{}, fmt.Errorf("decode Loki connection information: %w", err)
	}
	if connection.SchemaVersion != 1 ||
		connection.LocalOrigin.Transport != "streamable-http" ||
		connection.LocalOrigin.Reachability != "loopback" ||
		connection.LocalOrigin.Authentication.Type != "bearer-token-file" {
		return ConnectionMaterial{}, errors.New("Loki connection information does not match Windows installer contract")
	}
	origin, port, err := parseLoopbackOrigin(connection.LocalOrigin.URL, false)
	if err != nil || port != options.MCPPort {
		return ConnectionMaterial{}, errors.New("Loki connection local origin does not match configured MCP port")
	}
	return ConnectionMaterial{
		LocalOrigin:        origin,
		Transport:          connection.LocalOrigin.Transport,
		Reachability:       connection.LocalOrigin.Reachability,
		AuthenticationType: connection.LocalOrigin.Authentication.Type,
		Token:              token,
	}, nil
}
