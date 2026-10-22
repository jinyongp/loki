package appvalues

import controlpolicy "loki/internal/control/policy"

// ApplicationIdentity is host selector metadata. Version 2 binds the selected
// generation and expected policy in the same atomic document.
type ApplicationIdentity struct {
	Version          int    `json:"version"`
	UID              uint32 `json:"uid"`
	GID              uint32 `json:"gid"`
	AgentUID         uint32 `json:"agent_uid"`
	MetadataGID      uint32 `json:"metadata_gid"`
	DeploymentID     string `json:"deployment_id"`
	RunnerID         string `json:"runner_id"`
	ActiveGeneration string `json:"active_generation"`
	PolicySHA256     string `json:"policy_sha256,omitempty"`
}

// MigrationKeyMapping requires an explicit public kind. An omitted Kind means
// secret; an omitted Name preserves the source key without guessing a rename.
type MigrationKeyMapping struct {
	Source string `json:"source"`
	Name   string `json:"name,omitempty"`
	Kind   string `json:"kind,omitempty"`
}

type MigrationProfileMapping struct {
	Profile     string                `json:"profile"`
	Resource    string                `json:"resource"`
	Environment string                `json:"environment"`
	Keys        []MigrationKeyMapping `json:"keys"`
}

// MigrationPlan contains public metadata only. Source decoding/coverage and
// private byte validation stay with the migration source owner.
type MigrationPlan struct {
	Version   int                                 `json:"version"`
	Revision  uint64                              `json:"source_revision"`
	Mappings  []MigrationProfileMapping           `json:"mappings"`
	Resources []controlpolicy.ApplicationResource `json:"resources"`
	Values    int                                 `json:"value_count"`
}

// MigrationComparisonBinding is private host-operation metadata checkpointed
// before approving a read-only migration delivery. It is not an OCI reservation.
type MigrationComparisonBinding struct {
	Operation     string   `json:"operation"`
	Attempt       string   `json:"attempt"`
	Policy        string   `json:"policy"`
	Resource      string   `json:"resource"`
	Environment   string   `json:"env"`
	Names         []string `json:"names"`
	Runner        string   `json:"runner"`
	Instance      string   `json:"instance"`
	RootID        string   `json:"root_id"`
	VaultID       string   `json:"vault_id"`
	Execution     string   `json:"execution"`
	Input         string   `json:"input"`
	CorrelationID string   `json:"correlation_id"`
	Revision      uint64   `json:"revision"`
}

type MigrationPrepared struct {
	Stage   string                     `json:"stage"`
	Binding MigrationComparisonBinding `json:"binding"`
}

type MigrationApproval struct {
	Stage     string `json:"stage"`
	Attempt   string `json:"attempt"`
	Execution string `json:"execution"`
	Input     string `json:"input"`
	Revision  uint64 `json:"revision"`
}

type MigrationComparisonResult struct {
	Stage   string `json:"stage"`
	Matched bool   `json:"matched"`
	Count   int    `json:"count"`
}
