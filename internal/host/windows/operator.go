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
	return client.execute(ctx, distribution, request, nil)
}

func (client OperatorClient) ExecuteStreaming(
	ctx context.Context,
	distribution string,
	request OperatorRequest,
	reporter progress.Reporter,
) (OperatorResult, error) {
	return client.execute(ctx, distribution, request, reporter)
}

func (client OperatorClient) execute(
	ctx context.Context,
	distribution string,
	request OperatorRequest,
	reporter progress.Reporter,
) (OperatorResult, error) {
	if !distributionNamePattern.MatchString(distribution) {
		return OperatorResult{}, errors.New("Windows Loki distribution name is invalid")
	}
	version, err := client.WSL.OwnedDistributionVersion(ctx, distribution)
	if err != nil {
		return OperatorResult{}, err
	}
	command, machine, err := operatorCommandArguments(request)
	if err != nil {
		return OperatorResult{}, err
	}
	arguments := []string{"-d", distribution, "--user", "root", "--exec", "/usr/local/bin/loki"}
	arguments = append(arguments, command...)
	var result NativeProbe
	if reporter == nil {
		result, err = client.WSL.run(ctx, arguments...)
	} else {
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
	return OperatorResult{Probe: result, DistributionVersion: version}, nil
}

func operatorCommandArguments(request OperatorRequest) ([]string, bool, error) {
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
