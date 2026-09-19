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
		FlatActionUnionCount:   13,
		InputPropertyCount:     160,
		DescribedPropertyCount: 54,
		MissingOutputSchema:    3,
		OpenOutputSchema:       17,
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

	migrated := map[string]bool{
		"system_inspect": true, "workspace_read": true, "workspace_edit": true,
		"restore_workspace_file": true, "remove_tracked_file": true, "git_stage": true,
	}
	for _, issue := range audit.Issues {
		if !migrated[issue.Tool] {
			continue
		}
		if issue.Code == "flat_action_union" || issue.Code == "missing_field_description" ||
			(issue.Code == "open_output_schema" && (issue.Tool == "workspace_read" || issue.Tool == "workspace_edit" || issue.Tool == "restore_workspace_file" || issue.Tool == "remove_tracked_file")) {
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
