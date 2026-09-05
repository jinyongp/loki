package action

import (
	"errors"
	"fmt"
	"syscall"
)

type executionStageError struct {
	stage string
	cause error
}

func (e executionStageError) Error() string {
	var errno syscall.Errno
	if errors.As(e.cause, &errno) {
		return fmt.Sprintf("action execution failed [%s, errno=%d]", e.stage, errno)
	}
	return "action execution failed [" + e.stage + "]"
}

// PublicExecutionError exposes fixed stage names and numeric errno only. Never
// include raw errors, arguments, environment values, or credential payloads.
func PublicExecutionError(err error) string {
	var stage executionStageError
	if errors.As(err, &stage) {
		return stage.Error()
	}
	return "action execution failed"
}
