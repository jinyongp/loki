package process

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"loki/internal/redact"
)

func TestManagedSecretOutputNeverExposesPageFragments(t *testing.T) {
	m, err := NewManager(ManagerOptions{MaxProcesses: 1, MaxOutputBytes: 4096, Retention: time.Minute, RequireRedactor: true})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err = m.Start(StartSpec{Spec: Spec{Argv: []string{"/usr/bin/true"}, Timeout: time.Second, MaxOutput: 4096}}); err == nil {
		t.Fatal("private process accepted without a redactor")
	}
	value := "fixture-secret"
	filter, err := redact.New([]string{value})
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.Start(StartSpec{Name: "synthetic-private", Redactor: filter, Spec: Spec{
		Argv: []string{"/bin/sh", "-c", "printf '%s' \"${TOKEN%secret}\"; sleep 0.2; printf 'secret done!'"},
		CWD:  t.TempDir(), Env: []string{"PATH=/usr/bin:/bin", "TOKEN=" + value}, Timeout: 5 * time.Second, MaxOutput: 8,
	}})
	if err != nil {
		t.Fatal(err)
	}
	id := r["session_id"].(string)
	r = awaitOutput(t, m, id, "[REDACTED]")
	if strings.Contains(r["output"].(string), "fixture") {
		t.Fatal("partial first write leaked")
	}
	p, _ := m.get(id)
	awaitComplete(t, p)
	zero := int64(0)
	r, err = m.Read(id, &zero, 4096)
	if err != nil || r["output"] != "[REDACTED] done!" || r["available_from"] != int64(12) || r["next_offset"] != int64(20) || r["output_lost"] != true {
		t.Fatalf("redacted tail cursor: %#v %v", r, err)
	}
	for _, offset := range []int64{12, 13} {
		r, err = m.Read(id, &offset, 1)
		if err != nil || r["output"] != "[REDACTED]" || r["next_offset"] != offset+1 {
			t.Fatalf("partial secret page: %#v %v", r, err)
		}
	}
	for _, result := range []map[string]any{r, m.List(), m.Usage()} {
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), value) {
			t.Fatal("secret escaped public manager response")
		}
	}
}
