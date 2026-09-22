package runtime

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/agentcontext"
	controlpolicy "loki/internal/control/policy"
)

func serviceContextDraft() agentcontext.ContextDraft {
	return agentcontext.ContextDraft{
		Basis: agentcontext.ContextBasis{
			RepositoryID:            strings.Repeat("a", 64),
			WorktreeID:              strings.Repeat("b", 64),
			WorkstreamID:            "11111111-1111-4111-8111-111111111111",
			TaskID:                  "22222222-2222-4222-8222-222222222222",
			RunID:                   "33333333-3333-4333-8333-333333333333",
			CoordinationFingerprint: strings.Repeat("c", 64),
			CodeBasis:               strings.Repeat("d", 64),
			GuidanceRevision:        strings.Repeat("e", 64),
			Skills: []agentcontext.ContextSkillRevision{
				{Name: "review-skill", Scope: "project", Revision: strings.Repeat("f", 64)},
			},
			ValidationRecordIDs: []string{"44444444-4444-4444-8444-444444444444"},
			EvidenceRefs:        []string{"git:status:fixture"},
			Gaps:                []string{},
		},
		Summary:    "Progress",
		Decisions:  []string{"Keep canonical task truth external."},
		Remaining:  []string{"Resume composition"},
		Blockers:   []string{},
		NextAction: "Implement project context.",
		SessionRef: strings.Repeat("a", 16),
	}
}

func TestContextJournalRuntimeOperationsAreTypedAndReplaySafe(t *testing.T) {
	journal, err := agentcontext.NewContextJournal(filepath.Join(t.TempDir(), "context"), agentcontext.ContextJournalLimits{})
	if err != nil {
		t.Fatal(err)
	}
	ops := ContextJournalOperations(journal)
	for name, operation := range ops {
		if operation.Grant != controlpolicy.Agent {
			t.Fatalf("%s grant = %v", name, operation.Grant)
		}
	}

	draft := serviceContextDraft()
	putInput, err := json.Marshal(map[string]any{
		"operation":         "context_checkpoint_put",
		"request_id":        "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"expected_previous": "missing",
		"draft":             draft,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ops["context_checkpoint_put"].Handle(t.Context(), putInput)
	if err != nil {
		t.Fatal(err)
	}
	put := raw.(agentcontext.ContextPutResult)
	if put.Replayed || put.Record.ID == "" {
		t.Fatalf("put = %#v", put)
	}
	replayedRaw, err := ops["context_checkpoint_put"].Handle(t.Context(), putInput)
	if err != nil {
		t.Fatal(err)
	}
	replayed := replayedRaw.(agentcontext.ContextPutResult)
	if !replayed.Replayed || replayed.Record.ID != put.Record.ID {
		t.Fatalf("replay = %#v", replayed)
	}

	scope := map[string]any{
		"operation":     "context_checkpoint_latest",
		"repository_id": draft.Basis.RepositoryID,
		"worktree_id":   draft.Basis.WorktreeID,
		"workstream_id": draft.Basis.WorkstreamID,
	}
	latestInput, _ := json.Marshal(scope)
	latestRaw, err := ops["context_checkpoint_latest"].Handle(t.Context(), latestInput)
	if err != nil {
		t.Fatal(err)
	}
	latest := latestRaw.(agentcontext.ContextLatestResult)
	if !latest.Found || latest.Record == nil || latest.Record.ID != put.Record.ID {
		t.Fatalf("latest = %#v", latest)
	}

	scope["operation"] = "context_checkpoint_get"
	scope["record_id"] = put.Record.ID
	getInput, _ := json.Marshal(scope)
	getRaw, err := ops["context_checkpoint_get"].Handle(t.Context(), getInput)
	if err != nil {
		t.Fatal(err)
	}
	got := getRaw.(agentcontext.ContextLatestResult)
	if !got.Found || got.Record == nil || got.Record.ID != put.Record.ID {
		t.Fatalf("get = %#v", got)
	}

	delete(scope, "record_id")
	scope["operation"] = "context_checkpoint_list"
	scope["limit"] = 20
	listInput, _ := json.Marshal(scope)
	listRaw, err := ops["context_checkpoint_list"].Handle(t.Context(), listInput)
	if err != nil {
		t.Fatal(err)
	}
	listed := listRaw.(agentcontext.ContextListResult)
	if !listed.Complete || len(listed.Records) != 1 {
		t.Fatalf("list = %#v", listed)
	}
}

func TestContextJournalRuntimeOperationsRejectPrivateAndIrrelevantFields(t *testing.T) {
	journal, err := agentcontext.NewContextJournal(filepath.Join(t.TempDir(), "context"), agentcontext.ContextJournalLimits{})
	if err != nil {
		t.Fatal(err)
	}
	ops := ContextJournalOperations(journal)
	draft := serviceContextDraft()
	for _, field := range []string{"context", "session_id", "environment", "secret"} {
		input, err := json.Marshal(map[string]any{
			"operation":         "context_checkpoint_put",
			"request_id":        "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			"expected_previous": "missing",
			"draft":             draft,
			field:               "private-canary",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ops["context_checkpoint_put"].Handle(t.Context(), input); err == nil {
			t.Fatalf("context put accepted %q", field)
		}
	}

	latest, _ := json.Marshal(map[string]any{
		"operation":     "context_checkpoint_latest",
		"repository_id": draft.Basis.RepositoryID,
		"worktree_id":   draft.Basis.WorktreeID,
		"workstream_id": draft.Basis.WorkstreamID,
		"record_id":     strings.Repeat("1", 64),
	})
	if _, err := ops["context_checkpoint_latest"].Handle(t.Context(), latest); err == nil {
		t.Fatal("latest accepted record_id")
	}
	if _, err := ContextJournalOperations(nil)["context_checkpoint_latest"].Handle(t.Context(), latest); err == nil {
		t.Fatal("unavailable context journal was accepted")
	}
}
