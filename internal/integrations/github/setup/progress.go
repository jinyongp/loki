package githubsetup

import (
	"context"
	"fmt"
	"io"
	"strings"

	"loki/internal/progress"
)

// Announce work before calling a transport: WSL startup and GitHub requests can
// both take time before the backend has anything to report.
func requestWithProgress[T any](ctx context.Context, output io.Writer, message string, request func() (T, error)) (T, error) {
	fmt.Fprintln(output, message)
	stop := progress.StartHeartbeat(ctx, progress.NewLineReporter(output), progress.HeartbeatOptions{
		Operation: "github-setup", Phase: "request", Message: strings.TrimSuffix(message, "..."),
	})
	defer stop()
	return request()
}
