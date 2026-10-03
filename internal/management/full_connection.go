package management

import "context"

// FullConnection contains an owned-container stdio command, never a bearer.
// It is returned only to the management transport, not public status reports.
type FullConnection struct {
	Command     []string
	Environment []string
}

type FullConnectionBackend interface {
	Connection(context.Context) (FullConnection, error)
}
