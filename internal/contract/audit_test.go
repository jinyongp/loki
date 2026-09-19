package contract

import "testing"

func TestCurrentContractAuditTracksMigrationDebt(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	audit, err := AuditDefinitions(definitions)
	if err != nil {
		t.Fatal(err)
	}
	want := ContractAudit{
		ToolCount:              32,
		ActionUnionCount:       19,
		FlatActionUnionCount:   14,
		InputPropertyCount:     157,
		DescribedPropertyCount: 38,
		MissingOutputSchema:    3,
		OpenOutputSchema:       21,
		MissingAnnotations:     1,
	}
	if audit.ToolCount != want.ToolCount ||
		audit.ActionUnionCount != want.ActionUnionCount ||
		audit.FlatActionUnionCount != want.FlatActionUnionCount ||
		audit.InputPropertyCount != want.InputPropertyCount ||
		audit.DescribedPropertyCount != want.DescribedPropertyCount ||
		audit.MissingOutputSchema != want.MissingOutputSchema ||
		audit.OpenOutputSchema != want.OpenOutputSchema ||
		audit.MissingAnnotations != want.MissingAnnotations {
		t.Fatalf("contract audit counts = %#v, want %#v", audit, want)
	}

	for _, issue := range audit.Issues {
		if issue.Tool != "system_inspect" && issue.Tool != "workspace_edit" && issue.Tool != "git_stage" {
			continue
		}
		if issue.Code == "flat_action_union" || issue.Code == "missing_field_description" {
			t.Errorf("migrated tool regressed: %#v", issue)
		}
	}
}

func TestContractAuditIssueOrderIsStable(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	first, err := AuditDefinitions(definitions)
	if err != nil {
		t.Fatal(err)
	}
	second, err := AuditDefinitions(definitions)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Issues) != len(second.Issues) {
		t.Fatalf("issue count changed: %d != %d", len(first.Issues), len(second.Issues))
	}
	for index := range first.Issues {
		if first.Issues[index] != second.Issues[index] {
			t.Fatalf("issue order changed at %d: %#v != %#v", index, first.Issues[index], second.Issues[index])
		}
	}
}
