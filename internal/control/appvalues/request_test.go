package appvalues

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestActionFieldsCannotExpandAuthority(t *testing.T) {
	text := func(v string) *string { return &v }
	valid := Request{Action: "generate", Resource: text("demo"), Name: text("TOKEN"), ExpectedRevision: text(strings.Repeat("a", 64)), RequestID: text("159ff4f5-a75e-4d0b-8c6e-5c69ae9b527c")}
	if err := valid.Validate("write"); err != nil {
		t.Fatal(err)
	}
	for name, q := range map[string]Request{
		"variable payload": func() Request { q := valid; q.Value = text(""); return q }(),
		"kind override":    func() Request { q := valid; q.Kind = text("secret"); return q }(),
		"stage override":   func() Request { q := valid; q.StageID = text(strings.Repeat("a", 32)); return q }(),
		"empty env":        func() Request { q := valid; q.Environment = text(""); return q }(),
		"numeric revision": func() Request { q := valid; q.ExpectedRevision = text("1"); return q }(),
		"uppercase uuid":   func() Request { q := valid; q.RequestID = text(strings.ToUpper(*q.RequestID)); return q }(),
	} {
		t.Run(name, func(t *testing.T) {
			if q.Validate("write") == nil {
				t.Fatal("expanded mutation input admitted")
			}
		})
	}
	if valid.Env() != "common" || valid.PageLimit() != 50 {
		t.Fatal("omission defaults drifted")
	}
	// Public DTO serialization never invents legacy profile/length fields.
	raw, _ := json.Marshal(valid)
	for _, field := range []string{`"profile"`, `"bytes"`, `"secret"`} {
		if strings.Contains(string(raw), field) {
			t.Fatal("legacy authority field serialized")
		}
	}
}
