package windows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"loki/internal/progress"
)

const OperatorInfoSchemaVersion = 1

type OperatorRequest struct {
	Command             string
	Action              string
	BackupID            string
	Integration         string
	IdentityName        string
	IdentityEmail       string
	GitHubBrowser       bool
	GitHubUser          bool
	UseStdin            bool
	Approve             bool
	InterruptActiveJobs bool
}

type OperatorResult struct {
	Probe               NativeProbe
	DistributionVersion string
}

type OperatorClient struct {
	WSL WSLClient
}

func (client OperatorClient) Execute(
	ctx context.Context,
	distribution string,
	request OperatorRequest,
) (OperatorResult, error) {
	return client.execute(ctx, distribution, request, nil, nil)
}

func (client OperatorClient) ExecuteInput(
	ctx context.Context,
	distribution string,
	request OperatorRequest,
	input []byte,
) (OperatorResult, error) {
	return client.ExecuteInputStreaming(ctx, distribution, request, input, nil)
}

func (client OperatorClient) ExecuteInputStreaming(
	ctx context.Context,
	distribution string,
	request OperatorRequest,
	input []byte,
	reporter progress.Reporter,
) (OperatorResult, error) {
	if len(input) == 0 || len(input) > 4<<20 {
		return OperatorResult{}, errors.New("Windows operator stdin is empty or exceeds 4 MiB")
	}
	return client.execute(ctx, distribution, request, reporter, input)
}

func (client OperatorClient) ExecuteStreaming(
	ctx context.Context,
	distribution string,
	request OperatorRequest,
	reporter progress.Reporter,
) (OperatorResult, error) {
	return client.execute(ctx, distribution, request, reporter, nil)
}

func (client OperatorClient) execute(
	ctx context.Context,
	distribution string,
	request OperatorRequest,
	reporter progress.Reporter,
	input []byte,
) (OperatorResult, error) {
	if !distributionNamePattern.MatchString(distribution) {
		return OperatorResult{}, errors.New("Windows Loki distribution name is invalid")
	}
	version, err := client.WSL.OwnedDistributionVersion(ctx, distribution)
	if err != nil {
		return OperatorResult{}, err
	}
	if (input != nil) != request.UseStdin {
		return OperatorResult{}, errors.New("Windows operator stdin contract does not match request")
	}
	command, machine, err := operatorCommandArguments(request)
	if err != nil {
		return OperatorResult{}, err
	}
	arguments := []string{"-d", distribution, "--user", "root", "--exec", "/usr/local/bin/loki"}
	arguments = append(arguments, command...)
	var result NativeProbe
	switch {
	case input != nil:
		if reporter == nil {
			result, err = client.WSL.runInput(ctx, input, arguments...)
		} else {
			result, err = client.WSL.runInputStreaming(ctx, input, reporter, arguments...)
		}
	case reporter == nil:
		result, err = client.WSL.run(ctx, arguments...)
	default:
		result, err = client.WSL.runStreaming(ctx, reporter, arguments...)
	}
	if err != nil {
		return OperatorResult{}, err
	}
	if result.ExitCode == 0 && machine {
		if err := ValidateOperatorInfoSchema([]byte(result.Stdout)); err != nil {
			return OperatorResult{}, err
		}
	}
	if result.ExitCode == 0 && request.Command == "update" && request.Action == "apply" {
		// Compatibility bridge for appliances whose running manager predates
		// prerequisite reconciliation. The newly activated Linux binary owns
		// the repair policy; Windows only relays the already approved action.
		progress.Emit(reporter, progress.Event{Operation: "update", Phase: "appliance-prerequisites", State: progress.StateStarted, Message: "Checking WSL boot prerequisites..."})
		repairArgs := []string{"-d", distribution, "--user", "root", "--exec", "/usr/local/bin/loki", "host", "appliance", "repair", "--approve"}
		var repair NativeProbe
		if reporter == nil {
			repair, err = client.WSL.run(ctx, repairArgs...)
		} else {
			repair, err = client.WSL.runStreaming(ctx, reporter, repairArgs...)
		}
		if err != nil {
			return OperatorResult{}, err
		}
		if repair.ExitCode != 0 {
			return OperatorResult{}, nativeFailure("release applied but WSL prerequisite repair failed", repair)
		}
	}
	return OperatorResult{Probe: result, DistributionVersion: version}, nil
}

func operatorCommandArguments(request OperatorRequest) ([]string, bool, error) {
	if request.Command != "integration" &&
		(request.Integration != "" || request.IdentityName != "" || request.IdentityEmail != "" || request.UseStdin || request.GitHubBrowser || request.GitHubUser) {
		return nil, false, fmt.Errorf("%s does not accept integration options", request.Command)
	}
	switch request.Command {
	case "status":
		if request.Action != "" || request.BackupID != "" || request.Approve || request.InterruptActiveJobs {
			return nil, false, errors.New("status does not accept lifecycle mutation options")
		}
		return []string{"host", "status", "--system", "--json"}, true, nil
	case "doctor":
		if request.Action != "" || request.BackupID != "" || request.Approve || request.InterruptActiveJobs {
			return nil, false, errors.New("doctor does not accept lifecycle mutation options")
		}
		return []string{"host", "doctor", "--system"}, false, nil
	case "connection":
		if request.Action != "" || request.BackupID != "" || request.Approve || request.InterruptActiveJobs {
			return nil, false, errors.New("connection does not accept lifecycle mutation options")
		}
		return []string{"host", "connection", "--system", "--json"}, true, nil
	case "update":
		if request.Action != "status" && request.Action != "prepare" && request.Action != "apply" {
			return nil, false, errors.New("update action must be status, prepare, or apply")
		}
		if request.BackupID != "" {
			return nil, false, errors.New("update does not accept a backup id")
		}
		args := []string{"host", "update", request.Action, "--system"}
		if request.Action == "apply" {
			if !request.Approve {
				return nil, false, errors.New("update apply requires explicit approval")
			}
			args = append(args, "--approve")
			if request.InterruptActiveJobs {
				args = append(args, "--interrupt-active-jobs")
			}
		} else if request.Approve || request.InterruptActiveJobs {
			return nil, false, errors.New("update approval options are valid only for apply")
		}
		return args, false, nil
	case "backup", "rollback":
		if request.Action != "" || request.BackupID != "" || request.Approve {
			return nil, false, fmt.Errorf("%s accepts only --interrupt-active-jobs", request.Command)
		}
		args := []string{"host", request.Command, "--system"}
		if request.InterruptActiveJobs {
			args = append(args, "--interrupt-active-jobs")
		}
		return args, false, nil
	case "restore":
		if request.Action != "" || request.Approve {
			return nil, false, errors.New("restore accepts only BACKUP_ID and --interrupt-active-jobs")
		}
		backupID := strings.TrimSpace(request.BackupID)
		if backupID == "" || len(backupID) > 256 || strings.ContainsAny(backupID, "\r\n\x00") {
			return nil, false, errors.New("restore backup id is invalid")
		}
		args := []string{"host", "restore", "--system"}
		if request.InterruptActiveJobs {
			args = append(args, "--interrupt-active-jobs")
		}
		args = append(args, backupID)
		return args, false, nil
	case "integration":
		if request.GitHubUser {
			if request.Action != "login" || request.Integration != "github" || !request.UseStdin || request.GitHubBrowser ||
				request.IdentityName != "" || request.IdentityEmail != "" || request.BackupID != "" || request.Approve || request.InterruptActiveJobs {
				return nil, false, errors.New("GitHub user authorization relay requires login github with stdin")
			}
			return []string{"host", "integration", "login", "--system", "--browser-request", "github"}, false, nil
		}
		if request.GitHubBrowser && (request.Action != "setup" || request.Integration != "github" || !request.UseStdin || request.IdentityName != "" || request.IdentityEmail != "") {
			return nil, false, errors.New("GitHub browser relay requires setup github with stdin")
		}
		if request.BackupID != "" || request.Approve {
			return nil, false, errors.New("integration does not accept backup or approval options")
		}
		action := strings.TrimSpace(request.Action)
		if action != "list" && action != "status" && action != "doctor" &&
			action != "setup" && action != "rotate" && action != "import" && action != "enable" &&
			action != "disable" && action != "remove" {
			return nil, false, errors.New("integration action is invalid")
		}
		integration := strings.TrimSpace(request.Integration)
		if action == "list" {
			if integration != "" || request.IdentityName != "" || request.IdentityEmail != "" ||
				request.UseStdin || request.InterruptActiveJobs {
				return nil, false, errors.New("integration list does not accept integration mutation options")
			}
			return []string{"host", "integration", "list", "--system", "--json"}, true, nil
		}
		if integration != "browser" && integration != "signing" && integration != "github" {
			return nil, false, errors.New("integration name must be browser, signing, or github")
		}
		args := []string{"host", "integration", action, "--system"}
		switch action {
		case "status", "doctor":
			if request.IdentityName != "" || request.IdentityEmail != "" || request.UseStdin || request.InterruptActiveJobs {
				return nil, false, errors.New("integration inspection does not accept mutation options")
			}
			args = append(args, "--json", integration)
			return args, true, nil
		case "enable", "disable", "remove":
			if request.IdentityName != "" || request.IdentityEmail != "" || request.UseStdin {
				return nil, false, errors.New("integration mutation options are invalid")
			}
			if request.InterruptActiveJobs {
				args = append(args, "--interrupt-active-jobs")
			}
			args = append(args, integration)
			return args, false, nil
		case "setup", "rotate", "import":
			if request.InterruptActiveJobs {
				args = append(args, "--interrupt-active-jobs")
			}
			switch integration {
			case "browser":
				return nil, false, errors.New("browser does not require setup or rotation")
			case "signing":
				if request.Action == "import" {
					return nil, false, errors.New("integration import supports github only")
				}
				name := strings.TrimSpace(request.IdentityName)
				email := strings.TrimSpace(request.IdentityEmail)
				if name == "" || len(name) > 256 || strings.ContainsAny(name, "\r\n\x00") ||
					email == "" || len(email) > 320 || strings.ContainsAny(email, " \t\r\n\x00") {
					return nil, false, errors.New("signing identity is invalid")
				}
				args = append(args, "--identity-name", name, "--identity-email", email)
				if request.UseStdin {
					args = append(args, "--key-stdin")
				}
			case "github":
				if request.IdentityName != "" || request.IdentityEmail != "" || !request.UseStdin {
					return nil, false, errors.New("GitHub setup requires stdin and does not accept signing identity")
				}
				if request.GitHubBrowser {
					args = append(args, "--browser-request")
				} else {
					args = append(args, "--stdin")
				}
			}
			args = append(args, integration)
			return args, request.GitHubBrowser, nil
		}
		return nil, false, errors.New("integration action is invalid")
	default:
		return nil, false, fmt.Errorf("unsupported Windows operator command %q", request.Command)
	}
}

func ValidateOperatorInfoSchema(raw []byte) error {
	var envelope struct {
		SchemaVersion *int `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decode Loki operator information: %w", err)
	}
	if envelope.SchemaVersion == nil {
		return errors.New("Loki appliance operator information is missing schema_version")
	}
	if *envelope.SchemaVersion != OperatorInfoSchemaVersion {
		return fmt.Errorf("unsupported Loki appliance operator information schema %d", *envelope.SchemaVersion)
	}
	return nil
}

type OperatorStatus struct {
	SchemaVersion   int    `json:"schema_version"`
	State           string `json:"state"`
	Release         string `json:"release,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
	UpdatePrepared  bool   `json:"update_prepared"`
}

func ParseOperatorStatus(raw []byte) (OperatorStatus, error) {
	if err := ValidateOperatorInfoSchema(raw); err != nil {
		return OperatorStatus{}, err
	}
	var report OperatorStatus
	if err := json.Unmarshal(raw, &report); err != nil {
		return OperatorStatus{}, fmt.Errorf("decode Loki operator status: %w", err)
	}
	if strings.TrimSpace(report.State) == "" {
		return OperatorStatus{}, errors.New("Loki operator status is missing state")
	}
	return report, nil
}
