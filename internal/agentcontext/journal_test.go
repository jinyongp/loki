package agentcontext

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testWorkstreamID = "11111111-1111-4111-8111-111111111111"
	testTaskID       = "22222222-2222-4222-8222-222222222222"
	testRunID        = "33333333-3333-4333-8333-333333333333"
	testValidationID = "44444444-4444-4444-8444-444444444444"
)

func contextTestBasis() ContextBasis {
	return ContextBasis{
		RepositoryID:            strings.Repeat("a", 64),
		WorktreeID:              strings.Repeat("b", 64),
		WorkstreamID:            testWorkstreamID,
		TaskID:                  testTaskID,
		RunID:                   testRunID,
		CoordinationFingerprint: strings.Repeat("c", 64),
		CoordinationRevision:    "17",
		HistoryCursor:           "cursor-17",
		CodeBasis:               strings.Repeat("d", 64),
		GuidanceRevision:        strings.Repeat("e", 64),
		Skills: []ContextSkillRevision{
			{Name: "zeta-skill", Scope: "packaged", Revision: strings.Repeat("f", 64)},
			{Name: "alpha-skill", Scope: "project", Revision: strings.Repeat("1", 64)},
		},
		ValidationRecordIDs: []string{testValidationID},
		EvidenceRefs:        []string{"git:status:abc", "validation:local"},
		Gaps:                []string{"job.status.unavailable"},
	}
}

func contextTestDraft() ContextDraft {
	return ContextDraft{
		Basis:      contextTestBasis(),
		Summary:    "Implemented the current slice.",
		Decisions:  []string{"Keep canonical task truth in devtools."},
		Remaining:  []string{"Implement project context composition."},
		Blockers:   []string{},
		NextAction: "Implement the next bounded work item.",
		SessionRef: strings.Repeat("a", 16),
	}
}

func TestContextBasisFingerprintIsCanonicalAndSensitive(t *testing.T) {
	first := contextTestBasis()
	second := contextTestBasis()
	second.Skills[0], second.Skills[1] = second.Skills[1], second.Skills[0]
	second.EvidenceRefs = []string{"validation:local", "git:status:abc", "git:status:abc"}
	second.Gaps = []string{"job.status.unavailable", "job.status.unavailable"}

	left, err := ContextBasisFingerprint(first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := ContextBasisFingerprint(second)
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("canonical fingerprints differ: %s != %s", left, right)
	}

	second.CodeBasis = strings.Repeat("9", 64)
	changed, err := ContextBasisFingerprint(second)
	if err != nil {
		t.Fatal(err)
	}
	if changed == left {
		t.Fatal("authoritative basis change did not alter fingerprint")
	}

	nilCollections := contextTestBasis()
	nilCollections.Skills = nil
	nilCollections.ValidationRecordIDs = nil
	nilCollections.EvidenceRefs = nil
	nilCollections.Gaps = nil
	emptyCollections := nilCollections
	emptyCollections.Skills = []ContextSkillRevision{}
	emptyCollections.ValidationRecordIDs = []string{}
	emptyCollections.EvidenceRefs = []string{}
	emptyCollections.Gaps = []string{}
	nilHash, err := ContextBasisFingerprint(nilCollections)
	if err != nil {
		t.Fatal(err)
	}
	emptyHash, err := ContextBasisFingerprint(emptyCollections)
	if err != nil {
		t.Fatal(err)
	}
	if nilHash != emptyHash {
		t.Fatalf("nil and empty collections produced different fingerprints: %s != %s", nilHash, emptyHash)
	}
}

func newTestJournal(t *testing.T, root string, limits ContextJournalLimits) *ContextJournal {
	t.Helper()
	journal, err := NewContextJournal(filepath.Join(root, "context"), limits)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	step := 0
	journal.now = func() time.Time {
		value := base.Add(time.Duration(step) * time.Second)
		step++
		return value
	}
	return journal
}

func TestContextJournalPersistsCASAndReplaysRequestIDs(t *testing.T) {
	root := t.TempDir()
	journal := newTestJournal(t, root, ContextJournalLimits{})
	draft := contextTestDraft()

	first, err := journal.Put(context.Background(), "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "missing", draft)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || !contextDigestPattern.MatchString(first.Record.ID) ||
		first.Record.BasisFingerprint == "" || first.Record.DraftFingerprint == "" {
		t.Fatalf("first put = %#v", first)
	}

	replay, err := journal.Put(context.Background(), "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "missing", draft)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.Record.ID != first.Record.ID {
		t.Fatalf("replay = %#v", replay)
	}

	changed := draft
	changed.Summary = "Different input."
	if _, err := journal.Put(context.Background(), "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "missing", changed); err == nil ||
		!strings.Contains(err.Error(), "different input") {
		t.Fatalf("request-id reuse error = %v", err)
	}

	if _, err := journal.Put(context.Background(), "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", strings.Repeat("9", 64), draft); err == nil ||
		!strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale CAS error = %v", err)
	}

	secondDraft := draft
	secondDraft.Summary = "Second checkpoint."
	secondDraft.NextAction = "Continue from checkpoint two."
	second, err := journal.Put(context.Background(), "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", first.Record.ID, secondDraft)
	if err != nil {
		t.Fatal(err)
	}

	restarted, err := NewContextJournal(filepath.Join(root, "context"), ContextJournalLimits{})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := restarted.Latest(context.Background(), scopeFromBasis(draft.Basis))
	if err != nil {
		t.Fatal(err)
	}
	if !latest.Found || latest.Record == nil || latest.Record.ID != second.Record.ID {
		t.Fatalf("latest after restart = %#v", latest)
	}
	listed, err := restarted.List(context.Background(), scopeFromBasis(draft.Basis), 10)
	if err != nil {
		t.Fatal(err)
	}
	if !listed.Complete || len(listed.Records) != 2 ||
		listed.Records[0].ID != second.Record.ID || listed.Records[1].ID != first.Record.ID {
		t.Fatalf("listed = %#v", listed)
	}
	got, err := restarted.Get(context.Background(), scopeFromBasis(draft.Basis), first.Record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Found || got.Record == nil || got.Record.ID != first.Record.ID {
		t.Fatalf("get = %#v", got)
	}
}

func TestContextJournalRetentionReportsGapAndSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	limits := ContextJournalLimits{
		MaxRecordBytes: 64 << 10, MaxRecordsPerScope: 2, MaxScopeBytes: 256 << 10,
		MaxRecords: 4, MaxBytes: 512 << 10,
	}
	journal := newTestJournal(t, root, limits)
	draft := contextTestDraft()

	first, err := journal.Put(context.Background(), "10000000-0000-4000-8000-000000000001", "missing", draft)
	if err != nil {
		t.Fatal(err)
	}
	secondDraft := draft
	secondDraft.Summary = "second"
	second, err := journal.Put(context.Background(), "10000000-0000-4000-8000-000000000002", first.Record.ID, secondDraft)
	if err != nil {
		t.Fatal(err)
	}
	thirdDraft := draft
	thirdDraft.Summary = "third"
	third, err := journal.Put(context.Background(), "10000000-0000-4000-8000-000000000003", second.Record.ID, thirdDraft)
	if err != nil {
		t.Fatal(err)
	}
	if third.PrunedRecords != 1 || third.RetentionGap == nil || third.RetentionGap.PrunedRecords != 1 {
		t.Fatalf("third put retention = %#v", third)
	}

	listed, err := journal.List(context.Background(), scopeFromBasis(draft.Basis), 10)
	if err != nil {
		t.Fatal(err)
	}
	if listed.Complete || len(listed.Records) != 2 || listed.RetentionGap == nil ||
		listed.RetentionGap.PrunedRecords != 1 {
		t.Fatalf("retained list = %#v", listed)
	}
	missing, err := journal.Get(context.Background(), scopeFromBasis(draft.Basis), first.Record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if missing.Found || missing.RetentionGap == nil {
		t.Fatalf("pruned get = %#v", missing)
	}

	restarted, err := NewContextJournal(filepath.Join(root, "context"), limits)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := restarted.Latest(context.Background(), scopeFromBasis(draft.Basis))
	if err != nil {
		t.Fatal(err)
	}
	if !latest.Found || latest.Record == nil || latest.Record.ID != third.Record.ID ||
		latest.RetentionGap == nil || latest.RetentionGap.PrunedRecords != 1 {
		t.Fatalf("latest after restart = %#v", latest)
	}
}

func TestContextJournalReconcilesPendingPruneAfterRestart(t *testing.T) {
	root := t.TempDir()
	journal := newTestJournal(t, root, ContextJournalLimits{})
	draft := contextTestDraft()
	first, err := journal.Put(context.Background(), "20000000-0000-4000-8000-000000000001", "missing", draft)
	if err != nil {
		t.Fatal(err)
	}
	secondDraft := draft
	secondDraft.Summary = "second"
	second, err := journal.Put(context.Background(), "20000000-0000-4000-8000-000000000002", first.Record.ID, secondDraft)
	if err != nil {
		t.Fatal(err)
	}

	state, err := journal.loadRetention()
	if err != nil {
		t.Fatal(err)
	}
	state.Pending = []contextPendingPrune{{ScopeKey: first.Record.ScopeKey, RecordID: first.Record.ID}}
	if err := journal.publishRetention(state); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewContextJournal(filepath.Join(root, "context"), ContextJournalLimits{})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := restarted.Latest(context.Background(), scopeFromBasis(draft.Basis))
	if err != nil {
		t.Fatal(err)
	}
	if !latest.Found || latest.Record == nil || latest.Record.ID != second.Record.ID ||
		latest.RetentionGap == nil || latest.RetentionGap.PrunedRecords != 1 {
		t.Fatalf("reconciled latest = %#v", latest)
	}
	if _, err := os.Stat(filepath.Join(root, "context", "records", first.Record.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("pending prune record still exists: %v", err)
	}
}

func TestContextJournalRejectsCorruptionAndUnsafeDrafts(t *testing.T) {
	root := t.TempDir()
	journal := newTestJournal(t, root, ContextJournalLimits{})
	draft := contextTestDraft()

	put, err := journal.Put(context.Background(), "30000000-0000-4000-8000-000000000001", "missing", draft)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "context", "records", put.Record.ID+".json")
	if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Latest(context.Background(), scopeFromBasis(draft.Basis)); err == nil ||
		!strings.Contains(err.Error(), "record") {
		t.Fatalf("corruption error = %v", err)
	}

	unsafe := contextTestDraft()
	unsafe.Summary = "contains\x00nul"
	cleanJournal := newTestJournal(t, t.TempDir(), ContextJournalLimits{})
	if _, err := cleanJournal.Put(context.Background(), "30000000-0000-4000-8000-000000000002", "missing", unsafe); err == nil {
		t.Fatal("unsafe narrative was accepted")
	}
	unsafe = contextTestDraft()
	unsafe.SessionRef = "raw-session-id"
	if _, err := cleanJournal.Put(context.Background(), "30000000-0000-4000-8000-000000000003", "missing", unsafe); err == nil {
		t.Fatal("raw session identifier was accepted")
	}
}

func TestContextJournalRebuildsRetentionAfterUnrecordedOverage(t *testing.T) {
	root := t.TempDir()
	wideLimits := ContextJournalLimits{
		MaxRecordBytes: 64 << 10, MaxRecordsPerScope: 3, MaxScopeBytes: 256 << 10,
		MaxRecords: 8, MaxBytes: 512 << 10,
	}
	journal := newTestJournal(t, root, wideLimits)
	draft := contextTestDraft()

	first, err := journal.Put(context.Background(), "40000000-0000-4000-8000-000000000001", "missing", draft)
	if err != nil {
		t.Fatal(err)
	}
	secondDraft := draft
	secondDraft.Summary = "second"
	second, err := journal.Put(context.Background(), "40000000-0000-4000-8000-000000000002", first.Record.ID, secondDraft)
	if err != nil {
		t.Fatal(err)
	}
	thirdDraft := draft
	thirdDraft.Summary = "third"
	third, err := journal.Put(context.Background(), "40000000-0000-4000-8000-000000000003", second.Record.ID, thirdDraft)
	if err != nil {
		t.Fatal(err)
	}
	if third.PrunedRecords != 0 {
		t.Fatalf("wide journal pruned unexpectedly: %#v", third)
	}

	narrowLimits := wideLimits
	narrowLimits.MaxRecordsPerScope = 2
	restarted, err := NewContextJournal(filepath.Join(root, "context"), narrowLimits)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := restarted.Latest(context.Background(), scopeFromBasis(draft.Basis))
	if err != nil {
		t.Fatal(err)
	}
	if !latest.Found || latest.Record == nil || latest.Record.ID != third.Record.ID ||
		latest.RetentionGap == nil || latest.RetentionGap.PrunedRecords != 1 {
		t.Fatalf("recovered retention = %#v", latest)
	}
	if _, err := os.Stat(filepath.Join(root, "context", "records", first.Record.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("old record survived reconstructed retention: %v", err)
	}
}

func TestContextJournalOrdersRFC3339NanoByTimeNotText(t *testing.T) {
	root := t.TempDir()
	journal, err := NewContextJournal(filepath.Join(root, "context"), ContextJournalLimits{})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	times := []time.Time{base, base.Add(100 * time.Millisecond)}
	journal.now = func() time.Time {
		value := times[0]
		times = times[1:]
		return value
	}
	draft := contextTestDraft()
	first, err := journal.Put(context.Background(), "50000000-0000-4000-8000-000000000001", "missing", draft)
	if err != nil {
		t.Fatal(err)
	}
	secondDraft := draft
	secondDraft.Summary = "later in the same second"
	second, err := journal.Put(context.Background(), "50000000-0000-4000-8000-000000000002", first.Record.ID, secondDraft)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewContextJournal(filepath.Join(root, "context"), ContextJournalLimits{})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := restarted.Latest(context.Background(), scopeFromBasis(draft.Basis))
	if err != nil {
		t.Fatal(err)
	}
	if !latest.Found || latest.Record == nil || latest.Record.ID != second.Record.ID {
		t.Fatalf("same-second latest = %#v", latest)
	}
}
