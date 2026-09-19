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
		FlatActionUnionCount:   8,
		InputPropertyCount:     162,
		DescribedPropertyCount: 91,
		MissingOutputSchema:    3,
		OpenOutputSchema:       9,
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
		"system_inspect": true, "preview_publish": true, "shared_resources": true, "revoke_share": true,
		"browser_session": true, "browser_observe": true, "browser_interact": true,
		"workspace_read": true, "workspace_edit": true,
		"restore_workspace_file": true, "remove_tracked_file": true,
		"git_inspect": true, "git_stage": true,
	}
	for _, issue := range audit.Issues {
		if !migrated[issue.Tool] {
			continue
		}
		if issue.Code == "flat_action_union" || issue.Code == "missing_field_description" ||
			(issue.Code == "open_output_schema" && (issue.Tool == "preview_publish" || issue.Tool == "shared_resources" || issue.Tool == "revoke_share" ||
				issue.Tool == "browser_session" || issue.Tool == "browser_observe" || issue.Tool == "browser_interact" ||
				issue.Tool == "workspace_read" || issue.Tool == "workspace_edit" ||
				issue.Tool == "restore_workspace_file" || issue.Tool == "remove_tracked_file" ||
				issue.Tool == "git_inspect" || issue.Tool == "git_stage")) {
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
