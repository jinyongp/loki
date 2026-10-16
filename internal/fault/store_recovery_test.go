package fault

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestStoreRecoveryPublicAdvice(t *testing.T) {
	for _, reason := range []string{"missing_key", "missing_data", "invalid_record", "conflicting_record", "unreadable_store", "initialization_pending"} {
		recovery := &StoreRecovery{Schema: 1, Owner: "secrets", Reason: reason}
		err := fmt.Errorf("private-file-and-value: %w", recovery)
		detail := Describe(WithCorrelation(err, "operation-id"))
		if detail.CorrelationID != "operation-id" || detail.Code != CodeUnavailable || strings.Contains(detail.Message, "private-file-and-value") || detail.NextAction == "" {
			t.Fatalf("unsafe/incomplete diagnostic: %+v", detail)
		}
		if detail.Retryable != (reason == "initialization_pending") {
			t.Fatalf("incorrect retry advice for %s", reason)
		}
		if !detail.Retryable && (!strings.Contains(detail.Message, "cannot prove") || !strings.Contains(detail.Message, "programs, settings and tool data")) {
			t.Fatalf("missing uncertainty/restore impact: %s", detail.Message)
		}
		var log bytes.Buffer
		WriteStoreRecovery(&log, err)
		if strings.Contains(log.String(), "private-file-and-value") {
			t.Fatal("diagnostic emitted private error context")
		}
		parsed := ParseStoreRecovery(log.Bytes())
		if parsed == nil || *parsed != *recovery {
			t.Fatal("diagnostic did not survive the process boundary")
		}
	}
}

func TestStoreRecoveryRejectsUntrustedLogContent(t *testing.T) {
	for _, log := range []string{
		`private arbitrary log`,
		StoreRecoveryPrefix + `{"schema":1,"owner":"private-path","reason":"missing_key"}`,
		StoreRecoveryPrefix + `{"schema":1,"owner":"github","reason":"private-secret"}`,
		StoreRecoveryPrefix + `{"schema":2,"owner":"github","reason":"missing_key"}`,
		StoreRecoveryPrefix + `{"schema":1,"owner":"github","reason":"missing_key","secret":"private-value"}`,
		StoreRecoveryPrefix + `{"schema":1,"owner":"github","reason":"missing_key"} trailing`,
		StoreRecoveryPrefix + strings.Repeat("x", 1025),
	} {
		if ParseStoreRecovery([]byte(log)) != nil {
			t.Fatal("accepted arbitrary or invalid diagnostic")
		}
	}
}
