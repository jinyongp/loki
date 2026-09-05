package action

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
)

func TestPublicExecutionErrorPrivacy(t *testing.T) {
	private := &os.PathError{Op: "write", Path: "/private/synthetic-secret", Err: syscall.EPERM}
	for _, test := range []struct {
		err  error
		want string
	}{
		{private, "action execution failed"},
		{executionStageError{"materialization-attach", private}, "action execution failed [materialization-attach, errno=1]"},
		{fmt.Errorf("private payload: %w", executionStageError{"sandbox-guards", errors.New("synthetic-secret")}), "action execution failed [sandbox-guards]"},
	} {
		if got := PublicExecutionError(test.err); got != test.want {
			t.Fatalf("public execution error: got %q, want %q", got, test.want)
		}
	}
}
