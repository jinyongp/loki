package appvalues

// AdminRequest carries private control metadata. Secret bytes use a separate
// bounded frame and cannot be represented in this administrative request.
type AdminRequest struct {
	Action        string   `json:"action"`
	Resource      string   `json:"resource,omitempty"`
	Environment   string   `json:"env,omitempty"`
	Principal     string   `json:"principal,omitempty"`
	Variables     []string `json:"variables,omitempty"`
	Members       []string `json:"members,omitempty"`
	Revision      string   `json:"expected_revision,omitempty"`
	RequestID     string   `json:"request_id,omitempty"`
	Name          string   `json:"name,omitempty"`
	Empty         bool     `json:"empty,omitempty"`
	Replace       bool     `json:"replace,omitempty"`
	Recipient     string   `json:"recipient,omitempty"`
	Operation     string   `json:"operation_id,omitempty"`
	ArchiveSHA256 string   `json:"archive_sha256,omitempty"`
}

// InputLimit describes only the private byte frame, never a public JSON value.
func (r AdminRequest) InputLimit() int {
	switch r.Action {
	case "backup_import_preview", "backup_import":
		return MaxApplicationRestoreInput
	case "stage":
		return 500000
	case "migration_compare":
		return 500000
	case "set_secret", "set_public":
		if !r.Empty {
			return 65536
		}
	case "protection_init", "protection_unlock":
		return 65536
	}
	return 0
}
