package appvalues

import controlpolicy "loki/internal/control/policy"

// BrokerLayout is frozen host-owned deployment metadata. It carries neither
// value bytes nor permission to select commands or roots through public IPC.
type BrokerLayout struct {
	UID          uint32 `json:"uid"`
	LauncherUID  uint32 `json:"launcher_uid"`
	Binary       string `json:"binary"`
	Root         string `json:"root"`
	Socket       string `json:"socket"`
	DeploymentID string `json:"deployment_id"`
	// JobDeploymentID binds the current OCI reservation. DeploymentID above
	// is the stable app-root lifetime used by mutation attribution/replay.
	JobDeploymentID string                              `json:"job_deployment_id,omitempty"`
	PolicySHA256    string                              `json:"policy_sha256"`
	RunnerID        string                              `json:"runner_id"`
	InstanceID      string                              `json:"instance_id,omitempty"`
	Resources       []controlpolicy.ApplicationResource `json:"resources"`
	BaseEnvironment map[string]string                   `json:"base_environment"`
	MetadataSocket  string                              `json:"metadata_socket,omitempty"`
	MetadataGID     uint32                              `json:"metadata_gid,omitempty"`
	AgentUID        uint32                              `json:"agent_uid,omitempty"`
	Principal       string                              `json:"principal,omitempty"`
	AdminSocket     string                              `json:"admin_socket,omitempty"`
	ApplicationRoot string                              `json:"application_root,omitempty"`
	Inbox           string                              `json:"inbox,omitempty"`
	RuntimeRoot     string                              `json:"runtime_root,omitempty"`
}
