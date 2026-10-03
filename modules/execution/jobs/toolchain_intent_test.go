package jobs

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeStartRequestBindsToolchainIntentToFingerprint(t *testing.T) {
	request := StartRequest{
		RequestID: "123e4567-e89b-12d3-a456-426614174300",
		CWD:       ".",
		Argv:      []string{"/bin/true"},
		Toolchains: []ToolchainRef{
			{Family: "pnpm", Version: "12.5.1", GenerationID: strings.Repeat("b", 64)},
			{Family: "node", Version: "26.9.0", GenerationID: strings.Repeat("a", 64)},
		},
	}
	normalized, fingerprint, err := NormalizeStartRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Toolchains) != 2 ||
		normalized.Toolchains[0].Family != "node" ||
		normalized.Toolchains[1].Family != "pnpm" {
		t.Fatalf("normalized toolchains = %#v", normalized.Toolchains)
	}
	changed := request
	changed.Toolchains = append([]ToolchainRef(nil), request.Toolchains...)
	changed.Toolchains[1].GenerationID = strings.Repeat("c", 64)
	_, changedFingerprint, err := NormalizeStartRequest(changed)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint == changedFingerprint {
		t.Fatal("toolchain generation change did not change Job replay fingerprint")
	}
	duplicate := request
	duplicate.Toolchains = []ToolchainRef{
		{Family: "node", Version: "26.9.0", GenerationID: strings.Repeat("a", 64)},
		{Family: "node", Version: "22.23.4", GenerationID: strings.Repeat("d", 64)},
	}
	if _, _, err = NormalizeStartRequest(duplicate); err == nil {
		t.Fatal("duplicate toolchain family was accepted")
	}
}

func TestJournalPersistsAndReplaysToolchainIntent(t *testing.T) {
	dir := privateJournalDir(t)
	journal := testJournal(t, dir)
	requestID := "123e4567-e89b-12d3-a456-426614174301"
	jobID, err := JobIDForRequestID(requestID)
	if err != nil {
		t.Fatal(err)
	}
	request := StartRequest{
		RequestID: requestID,
		CWD:       ".",
		Argv:      []string{"/bin/true"},
		Toolchains: []ToolchainRef{{
			Family: "node", Version: "26.9.0", GenerationID: strings.Repeat("a", 64),
		}},
	}
	normalized, fingerprint, err := NormalizeStartRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record, replayed, err := journal.AdmitRequestWithToolchains(
		jobID, "oci:"+strings.Repeat("e", 64), normalized.RequestID, fingerprint,
		normalized.Network, normalized.Endpoints, normalized.Toolchains, now.Add(time.Minute), now,
	)
	if err != nil || replayed || len(record.Toolchains) != 1 || record.Toolchains[0] != normalized.Toolchains[0] {
		t.Fatalf("admission = %#v replayed=%v err=%v", record, replayed, err)
	}
	replayedRecord, replayed, err := journal.AdmitRequestWithToolchains(
		jobID, "oci:"+strings.Repeat("e", 64), normalized.RequestID, fingerprint,
		normalized.Network, normalized.Endpoints, normalized.Toolchains, now.Add(time.Minute), now,
	)
	if err != nil || !replayed || len(replayedRecord.Toolchains) != 1 {
		t.Fatalf("replay = %#v replayed=%v err=%v", replayedRecord, replayed, err)
	}
	changed := append([]ToolchainRef(nil), normalized.Toolchains...)
	changed[0].GenerationID = strings.Repeat("f", 64)
	if _, _, err = journal.AdmitRequestWithToolchains(
		jobID, "oci:"+strings.Repeat("e", 64), normalized.RequestID, fingerprint,
		normalized.Network, normalized.Endpoints, changed, now.Add(time.Minute), now,
	); err == nil {
		t.Fatal("replay with changed toolchain generation was accepted")
	}
}
