package privateio

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestPrivateMigrationExpectedFramePreservesExactBytes(t *testing.T) {
	values := []MigrationExpectedValue{{Name: "EMPTY", Value: nil}, {Name: "TOKEN", Value: []byte("private-canary\n'\"<&>")}}
	body, err := EncodeMigrationExpected(values)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(body)
	decoded, err := DecodeMigrationExpected(body)
	if err != nil || len(decoded) != len(values) {
		t.Fatal(err)
	}
	for index, value := range decoded {
		if value.Name != values[index].Name || !bytes.Equal(value.Value, values[index].Value) {
			t.Fatal("private frame changed exact source bytes")
		}
		if _, err := json.Marshal(value); err == nil || fmt.Sprintf("%+v", value) != "[REDACTED]" {
			t.Fatal("ordinary serialization/formatting exposed private input")
		}
	}
	borrowed := decoded[1].Value
	ClearMigrationExpected(decoded)
	if !bytes.Equal(borrowed, make([]byte, len(borrowed))) {
		t.Fatal("decoded private bytes survived explicit cleanup")
	}
}

func TestPrivateMigrationFrameRejectsAmbiguousAndOversizedInput(t *testing.T) {
	for _, raw := range []string{
		`{"values":[{"name":"TOKEN","value":null}]}`,
		`{"values":[{"name":"TOKEN","value":"eA==","value":"eQ=="}]}`,
		`{"values":[{"name":"TOKEN","value":"eA==","extra":true}]}`,
		`{"values":[{"name":"TOKEN","value":"eA=="},{"name":"TOKEN","value":"eQ=="}]}`,
		`{"values":[]}`,
		`{"values":[{"name":"TOKEN","value":"AA=="}]}`,
	} {
		if decoded, err := DecodeMigrationExpected([]byte(raw)); err == nil {
			ClearMigrationExpected(decoded)
			t.Fatal("accepted invalid private frame")
		}
	}
	values := []MigrationExpectedValue{}
	for _, name := range []string{"A", "B", "C", "D", "E", "F"} {
		values = append(values, MigrationExpectedValue{Name: name, Value: []byte(strings.Repeat("x", 65536))})
	}
	if body, err := EncodeMigrationExpected(values); err == nil {
		clear(body)
		t.Fatal("accepted an over-budget private batch")
	}
}
