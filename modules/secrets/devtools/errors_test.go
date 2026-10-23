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

func TestProviderDiagnosticsAcceptOnlyClosedFacts(t *testing.T) {
	err := providerFailure([]byte(`{"error":{"code":"redaction_unavailable","message":"private","details":{"retry":"after_input_change","effects":"none","facts":{"limit_kind":"states","observed":262145,"limit":262144},"target":{"request_id":"c1587d89-9336-4af5-95ab-f386f9080e04","path":"private"},"remedies":[{"argv":["private"]}]}}}`))
	f := err.(*Failure)
	if f.Limit == nil || !f.Limit.Valid() || f.Retry != "after_input_change" || f.Effects != "none" || f.RequestID == "" {
		t.Fatal("reviewed diagnostics lost")
	}
	for _, facts := range []string{`{"limit_kind":"private","observed":262145,"limit":262144}`, `{"limit_kind":"states","observed":262144,"limit":262144}`, `{"limit_kind":"states","observed":262145,"limit":1}`, `{"limit_kind":"states","observed":9007199254740992,"limit":262144}`} {
		e := providerFailure([]byte(`{"error":{"code":"redaction_unavailable","details":{"facts":` + facts + `,"retry":"private","effects":"private","target":{"request_id":"private"}}}}`)).(*Failure)
		if e.Limit != nil || e.Retry != "" || e.Effects != "" || e.RequestID != "" {
			t.Fatal("unreviewed diagnostic escaped")
		}
	}
}
