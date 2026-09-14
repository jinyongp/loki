package secret

import (
	"encoding/json"
	"testing"
)

func TestMigrateLegacyDocumentKeepsOnlyProfilesAndSecrets(t *testing.T) {
	legacy := json.RawMessage("{\"version\":1,\"profiles\":{\"web\":{\"secrets\":{\"TOKEN\":\"synthetic\"},\"actions\":{\"serve\":{\"command\":[\"node\"]}}}},\"projects\":{\"old\":{}},\"ignored\":true}")
	got, err := MigrateLegacyDocument(legacy)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"profiles\":{\"web\":{\"secrets\":{\"TOKEN\":\"synthetic\"}}},\"version\":1}"
	if string(got) != want {
		t.Fatalf("migrated document = %s, want %s", got, want)
	}
	if err = Validate(got); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateLegacyDocumentRejectsInvalidSecretState(t *testing.T) {
	for _, data := range []string{
		"{}",
		"{\"version\":1,\"profiles\":[]}",
		"{\"version\":1,\"profiles\":{\"Bad Profile\":{\"secrets\":{}}}}",
		"{\"version\":1,\"profiles\":{\"web\":{\"secrets\":{\"TOKEN\":1}}}}",
	} {
		if _, err := MigrateLegacyDocument(json.RawMessage(data)); err == nil {
			t.Fatalf("accepted invalid legacy document: %s", data)
		}
	}
}
