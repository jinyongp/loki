package audit

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestAuditReadPage(t *testing.T) {
	log := &Log{Path: filepath.Join(t.TempDir(), "audit.jsonl")}
	for i := 0; i < 5; i++ {
		if err := log.Runtime("operation", uint32(1000+i), i%2 == 0, nil); err != nil {
			t.Fatal(err)
		}
	}
	first, err := log.ReadPage(0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if first["offset"] != 0 || first["limit"] != 2 || first["total"] != 5 ||
		first["has_more"] != true || first["next_offset"] != 2 || first["complete"] != true {
		t.Fatalf("first audit page = %#v", first)
	}
	records := first["records"].([]json.RawMessage)
	if len(records) != 2 {
		t.Fatalf("first audit records = %#v", records)
	}
	var newest map[string]any
	if err := json.Unmarshal(records[0], &newest); err != nil {
		t.Fatal(err)
	}
	if newest["uid"] != float64(1004) {
		t.Fatalf("audit order = %#v", newest)
	}
	last, err := log.ReadPage(4, 2)
	if err != nil {
		t.Fatal(err)
	}
	if last["has_more"] != false || last["next_offset"] != nil || len(last["records"].([]json.RawMessage)) != 1 {
		t.Fatalf("last audit page = %#v", last)
	}
	if _, err = log.ReadPage(-1, 1); err == nil {
		t.Fatal("negative audit offset accepted")
	}
	if _, err = log.ReadPage(0, 201); err == nil {
		t.Fatal("oversized audit limit accepted")
	}
}
