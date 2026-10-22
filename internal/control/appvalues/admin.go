package appvalues

// AdminRequest carries private control metadata. Secret bytes use a separate
// bounded frame and cannot be represented in this administrative request.
type AdminRequest struct {
	Action      string   `json:"action"`
	Resource    string   `json:"resource"`
	Environment string   `json:"env,omitempty"`
	Principal   string   `json:"principal,omitempty"`
	Variables   []string `json:"variables,omitempty"`
	Members     []string `json:"members,omitempty"`
	Revision    string   `json:"expected_revision,omitempty"`
	RequestID   string   `json:"request_id,omitempty"`
	Name        string   `json:"name,omitempty"`
	Empty       bool     `json:"empty,omitempty"`
	Replace     bool     `json:"replace,omitempty"`
}
