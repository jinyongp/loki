package browser

import (
	"compress/gzip"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestChromiumDebugObservation(t *testing.T) {
	d, address := chromeDriver(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/data":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-API-Key", "response-secret")
			fmt.Fprint(w, `{"hello":"안녕하세요"}`)
		case "/missing":
			http.Error(w, "missing", 404)
		case "/large":
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "gzip")
			writer := gzip.NewWriter(w)
			fmt.Fprint(writer, strings.Repeat("A", 1048577))
			writer.Close()
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<title>Debug</title><script>
console.warn('fixture warning');
fetch('/data?token=hidden',{headers:{Authorization:'Bearer request-secret'}}).then(r=>r.text());
fetch('/missing');fetch('/large').then(r=>r.text());
setTimeout(()=>{throw new Error('fixture failure')},0);
</script>`)
		}
	}))
	callBrowser(t, d, "start", nil)
	callBrowser(t, d, "navigate", map[string]any{"url": address})
	var dataID, largeID string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		network := callBrowser(t, d, "network", nil)
		for _, request := range network["requests"].([]map[string]any) {
			url := request["url"].(string)
			if request["finished"] != true {
				continue
			}
			if strings.Contains(url, "/data?") {
				dataID = request["request_id"].(string)
				if strings.Contains(url, "hidden") {
					t.Fatal("query secret leaked")
				}
			}
			if strings.HasSuffix(url, "/large") {
				largeID = request["request_id"].(string)
			}
		}
		if dataID != "" && largeID != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if dataID == "" || largeID == "" {
		t.Fatal("fetch events missing")
	}
	detail := callBrowser(t, d, "request", map[string]any{"request_id": dataID, "include_body": true, "max_body_chars": 12})
	if detail["body"] != `{"hello":"안녕` || detail["body_truncated"] != true {
		t.Fatal(detail)
	}
	if headers := detail["request_headers"].(map[string]any); headers["Authorization"] != "[REDACTED]" {
		t.Fatal(headers)
	}
	if headers := detail["response_headers"].(map[string]any); headers["X-Api-Key"] != "[REDACTED]" {
		t.Fatal(headers)
	}
	if _, err := d.Call(t.Context(), "request", map[string]any{"request_id": largeID, "include_body": true}); err == nil {
		t.Fatal("compressed oversized body accepted")
	}
	if events := callBrowser(t, d, "console", map[string]any{"level": "warning"})["events"].([]map[string]any); len(events) != 1 {
		t.Fatal(events)
	}
	if events := callBrowser(t, d, "page_errors", nil)["errors"].([]map[string]any); len(events) != 1 {
		t.Fatal(events)
	}
	if requests := callBrowser(t, d, "network", map[string]any{"status_min": 400})["requests"].([]map[string]any); len(requests) != 1 {
		t.Fatal(requests)
	}
	if page := callBrowser(t, d, "debug_diagnostics", nil)["page"].(map[string]any); page["title"] != "Debug" {
		t.Fatal(page)
	}
	for _, args := range []map[string]any{{"limit": 0}, {"limit": true}, {"since_sequence": -1}, {"resource_type": 123}, {"status_min": 600}} {
		if _, err := d.Call(t.Context(), "network", args); err == nil {
			t.Fatal(args)
		}
	}
}
