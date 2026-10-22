package privateio

// JobAuthority contains durable public binding metadata, never environment
// entries, values, bearer handles or matcher state.
type JobAuthority struct {
	DeploymentID string `json:"deployment_id"`
	PolicySHA256 string `json:"policy_sha256"`
	Principal    string `json:"principal"`
	Repository   string `json:"repository"`
	ResourceID   string `json:"resource_id"`
	Resource     string `json:"resource"`
	Environment  string `json:"environment"`
}

type OperationIntent struct {
	RequestID string `json:"request_id"`
	Revision  uint64 `json:"revision"`
}

type ReconcileJobRequest struct {
	Authority   JobAuthority     `json:"authority"`
	JobID       string           `json:"job_id"`
	InstanceRef string           `json:"instance_ref"`
	Prepared    PreparedJob      `json:"prepared"`
	Action      string           `json:"action"`
	Fact        *JobFact         `json:"fact,omitempty"`
	Intent      *OperationIntent `json:"intent,omitempty"`
}

type JobRequest struct {
	Open      *JobOpen             `json:"open,omitempty"`
	Reconcile *ReconcileJobRequest `json:"reconcile,omitempty"`
}
