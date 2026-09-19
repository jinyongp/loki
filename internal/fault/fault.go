// Package fault distinguishes safe operational messages from private failures.
package fault

import (
	"context"
	"errors"
	"io/fs"
	"syscall"
)

type Error string

func (e Error) Error() string { return string(e) }

type Code string

const (
	CodeInvalidInput   Code = "invalid_input"
	CodeConflict       Code = "conflict"
	CodeDenied         Code = "denied"
	CodeUnavailable    Code = "unavailable"
	CodeQuotaExceeded  Code = "quota_exceeded"
	CodeFailed         Code = "failed"
	CodeOutcomeUnknown Code = "outcome_unknown"
)

type Detail struct {
	Code          Code   `json:"code"`
	Message       string `json:"message"`
	Retryable     bool   `json:"retryable"`
	CorrelationID string `json:"correlation_id,omitempty"`
	NextAction    string `json:"next_action,omitempty"`
}

type detailedError struct {
	detail Detail
	cause  error
}

func (e *detailedError) Error() string { return e.detail.Message }
func (e *detailedError) Unwrap() error { return e.cause }

func New(code Code, message string, retryable bool, nextAction string) error {
	if code == "" {
		code = CodeFailed
	}
	return &detailedError{detail: Detail{
		Code: code, Message: message, Retryable: retryable, NextAction: nextAction,
	}}
}

func WithCorrelation(err error, correlationID string) error {
	if err == nil || correlationID == "" {
		return err
	}
	detail := Describe(err)
	detail.CorrelationID = correlationID
	return &detailedError{detail: detail, cause: err}
}

func Describe(err error) Detail {
	var detailed *detailedError
	if errors.As(err, &detailed) {
		return detailed.detail
	}
	var safe Error
	if errors.As(err, &safe) {
		return Detail{Code: CodeFailed, Message: safe.Error()}
	}
	for _, item := range []struct {
		err    error
		detail Detail
	}{
		{fs.ErrNotExist, Detail{Code: CodeInvalidInput, Message: "requested path or required executable was not found; verify the path and installed tool", NextAction: "verify the requested path and installed tool"}},
		{fs.ErrExist, Detail{Code: CodeConflict, Message: "destination already exists; choose another path or explicitly allow overwrite", NextAction: "choose another path or explicitly allow overwrite"}},
		{fs.ErrPermission, Detail{Code: CodeDenied, Message: "workspace permissions denied the operation; verify ownership and writable scope", NextAction: "verify ownership and the authorized writable scope"}},
		{syscall.ENOTDIR, Detail{Code: CodeInvalidInput, Message: "a directory was required at the requested path"}},
		{syscall.EISDIR, Detail{Code: CodeInvalidInput, Message: "a regular file was required at the requested path"}},
		{context.DeadlineExceeded, Detail{Code: CodeUnavailable, Message: "operation timed out; narrow the request or increase its timeout", Retryable: true, NextAction: "narrow the request or retry with a larger allowed timeout"}},
		{syscall.ENOSPC, Detail{Code: CodeQuotaExceeded, Message: "workspace storage is full; free space and retry", NextAction: "free workspace storage before retrying"}},
		{syscall.EROFS, Detail{Code: CodeDenied, Message: "the target is read-only; write inside the workspace", NextAction: "choose an authorized writable target"}},
		{syscall.EBUSY, Detail{Code: CodeConflict, Message: "the requested resource is busy; stop the using process and retry", Retryable: true, NextAction: "stop the process using the resource before retrying"}},
		{syscall.ENAMETOOLONG, Detail{Code: CodeInvalidInput, Message: "the requested path or filename is too long"}},
		{syscall.EMFILE, Detail{Code: CodeUnavailable, Message: "the process has too many open files; close sessions and retry", Retryable: true, NextAction: "close unused sessions or resources before retrying"}},
	} {
		if errors.Is(err, item.err) {
			return item.detail
		}
	}
	return Detail{Code: CodeFailed, Message: "unexpected server failure; run diagnostics and retry", NextAction: "run diagnostics and inspect recent operation activity before retrying"}
}

func Public(err error) string {
	return Describe(err).Message
}
