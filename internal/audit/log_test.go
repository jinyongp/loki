package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentAuditAndMalformedTail(t *testing.T) {
	l := &Log{Path: filepath.Join(t.TempDir(), "audit.jsonl")}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.Runtime("devtools_call", 1000, true, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	result, err := l.Read(200)
	if err != nil || len(result["records"].([]json.RawMessage)) != 100 {
		t.Fatal(result, err)
	}
	f, err := os.OpenFile(l.Path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("broken\n{\"last\":true}\npartial")
	f.Close()
	result, err = l.Read(1)
	if err != nil {
		t.Fatal(err)
	}
	if string(result["records"].([]json.RawMessage)[0]) != `{"last":true}` {
		t.Fatal(result)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err = os.Symlink(l.Path, link); err != nil {
		t.Fatal(err)
	}
	if _, err = (&Log{Path: link}).Read(1); err == nil {
		t.Fatal("symlink accepted")
	}
}
