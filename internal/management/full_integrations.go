package management

import (
	"context"
	"encoding/json"
)

// IntegrationBackend keeps credentials inside the selected tool's protected
// data owner. Native management coordinates public setup and lifecycle only.
type IntegrationBackend interface {
	FullBackend
	ImportGitHub(context.Context, []byte, []byte) error
	SetupGitSigning(context.Context, string, string, []byte) (json.RawMessage, error)
	Administration(context.Context, any) (json.RawMessage, error)
}
