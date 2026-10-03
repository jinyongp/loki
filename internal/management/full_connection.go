package management

import "context"

// FullConnection contains local connection metadata, never the bearer itself.
// It is returned only to the management transport, not public status reports.
type FullConnection struct {
	Endpoint  string
	TokenFile string
}

type FullConnectionBackend interface {
	Connection(context.Context) (FullConnection, error)
}
