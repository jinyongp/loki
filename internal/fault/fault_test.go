package fault

import (
	"context"
	"errors"
	"io/fs"
	"testing"
)

func TestDescribeClassifiesPublicFailures(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		code      Code
		retryable bool
	}{
		{name: "missing", err: fs.ErrNotExist, code: CodeInvalidInput},
		{name: "permission", err: fs.ErrPermission, code: CodeDenied},
		{name: "timeout", err: context.DeadlineExceeded, code: CodeUnavailable, retryable: true},
		{name: "private", err: errors.New("private credential value"), code: CodeFailed},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			detail := Describe(test.err)
			if detail.Code != test.code || detail.Retryable != test.retryable {
				t.Fatalf("detail = %#v", detail)
			}
			if test.name == "private" && detail.Message == test.err.Error() {
				t.Fatal("private error text was exposed")
			}
		})
	}
}

func TestWithCorrelationPreservesTypedPublicError(t *testing.T) {
	err := New(CodeConflict, "resource changed", true, "inspect the current revision")
	err = WithCorrelation(err, "0123456789abcdef-0000000000000001")
	detail := Describe(err)
	if detail.Code != CodeConflict || detail.Message != "resource changed" || !detail.Retryable {
		t.Fatalf("detail = %#v", detail)
	}
	if detail.CorrelationID != "0123456789abcdef-0000000000000001" || detail.NextAction != "inspect the current revision" {
		t.Fatalf("correlation detail = %#v", detail)
	}
}
