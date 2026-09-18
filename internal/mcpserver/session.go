package mcpserver

import "context"

type sessionIDContextKey struct{}

func withSessionID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionIDContextKey{}, id)
}

// SessionID returns the server-issued MCP session binding for the current call.
// It is attribution/binding metadata only and never an authorization decision.
func SessionID(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(sessionIDContextKey{}).(string)
	return id, ok && id != ""
}
