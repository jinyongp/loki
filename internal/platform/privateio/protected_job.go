package privateio

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
)

// JobOpen is sent only by the authenticated launcher after durable admission
// and exact stopped-container binding. It carries no values or redaction data.
type JobOpen struct {
	Network         string             `json:"network"`
	Destinations    []string           `json:"destinations,omitempty"`
	CWD             string             `json:"cwd"`
	Protocol        int                `json:"protocol"`
	JobID           string             `json:"job_id"`
	DeploymentID    string             `json:"deployment_id"`
	RequestID       string             `json:"request_id"`
	RequestSHA256   string             `json:"request_sha256"`
	PolicySHA256    string             `json:"policy_sha256"`
	Principal       string             `json:"principal"`
	Repository      string             `json:"repository"`
	Resource        string             `json:"resource"`
	Environment     string             `json:"environment"`
	InstanceRef     string             `json:"instance_ref"`
	Argv            []string           `json:"argv"`
	BaseEnvironment []EnvironmentEntry `json:"base_environment"`
}

type JobFact struct {
	State       string `json:"state"`
	ExternalRef string `json:"external_ref"`
	ExitCode    *int64 `json:"exit_code,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

type ReportIntent struct {
	RequestID string  `json:"request_id"`
	Revision  uint64  `json:"revision"`
	Fact      JobFact `json:"fact"`
}

type ReportResult struct {
	Recorded bool   `json:"recorded"`
	Revision uint64 `json:"revision"`
}

func ExternalReference(instance string) string {
	sum := sha256.Sum256([]byte(instance))
	return "oci-reference-sha256:" + hex.EncodeToString(sum[:])
}

type PreparedJob struct {
	Stage            string `json:"stage"`
	ExecutionID      string `json:"execution_id"`
	InputID          string `json:"input_id"`
	RunnerID         string `json:"runner_id"`
	RunnerInstanceID string `json:"runner_instance_id"`
	Revision         uint64 `json:"revision"`
	PrepareRequestID string `json:"prepare_request_id"`
	ApproveRequestID string `json:"approve_request_id"`
	DeliverRequestID string `json:"deliver_request_id"`
}

type CommitJob struct {
	Stage       string `json:"stage"`
	JobID       string `json:"job_id"`
	ExecutionID string `json:"execution_id"`
	InputID     string `json:"input_id"`
}

// OperationID is RFC4122 UUIDv5 in a fixed Loki namespace. Deployment identity
// separates hosts; reusing a Job never allocates another upstream operation.
func OperationID(deployment, job, operation string) string {
	namespace := [16]byte{0x87, 0xba, 0x2d, 0x63, 0x75, 0xf1, 0x50, 0xa5, 0x8a, 0x48, 0x04, 0x67, 0xef, 0x8a, 0x99, 0xb4}
	h := sha1.New()
	h.Write(namespace[:])
	h.Write([]byte(deployment + "\x00" + job + "\x00" + operation))
	sum := h.Sum(nil)[:16]
	sum[6] = sum[6]&0x0f | 0x50
	sum[8] = sum[8]&0x3f | 0x80
	s := hex.EncodeToString(sum)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

const (
	StreamStdout byte = 1
	StreamStderr byte = 2
	StreamEOF    byte = 3
)
