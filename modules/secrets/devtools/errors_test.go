package devtools

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestProviderErrorsKeepOnlyReviewedIdentifiers(t *testing.T) {
	err := providerFailure([]byte(`{"error":{"code":"vault_locked","message":"canary-private-value","details":{"reason":"canary-private-value","path":"/private/root"},"remedies":["canary-private-value"]}}`))
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "vault_locked" || failure.Reason != "" || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unsafe failure conversion: %v", err)
	}
	if strings.Contains(fmt.Sprintf("%+v", err), "canary") || strings.Contains(err.Error(), "/private") {
		t.Fatal("upstream private diagnostics escaped")
	}
	err = providerFailure([]byte(`{"error":{"code":"canary-private-value"}}`))
	if err.Error() != "application value provider: unavailable" {
		t.Fatal("unreviewed failure identifier escaped")
	}
}
