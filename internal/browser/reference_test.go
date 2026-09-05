package browser

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"loki/internal/cdp"
)

func TestPython0471BrowserDebugDifferential(t *testing.T) {
	python := os.Getenv("LOKI_REFERENCE_PYTHON")
	if python == "" {
		t.Skip("Python reference not configured")
	}
	events := []struct {
		Method string
		Params map[string]any
	}{
		{"console_called", map[string]any{"type": "error", "args": []any{map[string]any{"value": "fixture"}, map[string]any{"value": true}}}},
		{"request_will_be_sent", map[string]any{"requestId": "1", "type": "Fetch", "request": map[string]any{"url": "https://example.test/?q=1&token=private", "headers": map[string]any{"Authorization": "private", "Accept": "text/plain"}}}},
		{"response_received", map[string]any{"requestId": "1", "type": "Fetch", "response": map[string]any{"status": 500, "mimeType": "text/plain", "headers": map[string]any{"Set-Cookie": "private"}}}},
		{"loading_finished", map[string]any{"requestId": "1", "encodedDataLength": 12}},
		{"exception_thrown", map[string]any{"exceptionDetails": map[string]any{"text": "failure", "url": "https://example.test/?api_key=private", "lineNumber": 2}}},
		{"websocket_created", map[string]any{"requestId": "ws", "url": "wss://example.test/?token=private"}},
	}
	methods := map[string]string{"console_called": "Runtime.consoleAPICalled", "request_will_be_sent": "Network.requestWillBeSent", "response_received": "Network.responseReceived", "loading_finished": "Network.loadingFinished", "exception_thrown": "Runtime.exceptionThrown", "websocket_created": "Network.webSocketCreated"}
	d := &Debug{}
	for _, event := range events {
		raw, _ := json.Marshal(event.Params)
		d.Event(cdp.Event{Method: methods[event.Method], Params: raw, SessionID: "session"})
	}
	got := []map[string]any{d.Events("console", "", 0, 100), d.Network(nil, false, "", 0, 100), d.Request("1"), d.Events("websockets", "", 0, 100), d.Events("page_errors", "", 0, 100), d.Diagnostics(0, 100), d.Events("console", "error", 0, 1), d.Network(nil, true, "fetch", 1, 100)}
	script := `import importlib.util,json,sys
spec=importlib.util.spec_from_file_location('reference_debug',sys.argv[1]);module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
d=module.BrowserDebugCollector()
for event in json.load(sys.stdin):getattr(d,event['Method'])(event['Params'],'session')
print(json.dumps([d.console_result(None,0,100),d.network_result(None,False,None,0,100),d.request_result('1'),d.websocket_result(0,100),d.page_errors_result(0,100),d.diagnostics_result(0,100),d.console_result('error',0,1),d.network_result(None,True,'fetch',1,100)]))
`
	source, err := filepath.Abs("../../browser_sidecar/src/loki_browser_sidecar/debug.py")
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(events)
	cmd := exec.Command(python, "-c", script, source)
	cmd.Stdin = bytes.NewReader(input)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v", raw, err)
	}
	var want, actual any
	if err = json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(got)
	if err = json.Unmarshal(encoded, &actual); err != nil {
		t.Fatal(err)
	}
	var normalize func(any)
	normalize = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			for key, child := range v {
				if key == "captured_at" {
					v[key] = "clock"
				} else {
					normalize(child)
				}
			}
		case []any:
			for _, child := range v {
				normalize(child)
			}
		}
	}
	normalize(want)
	normalize(actual)
	if !reflect.DeepEqual(actual, want) {
		a, _ := json.Marshal(actual)
		t.Fatalf("Go %s\nPython %s", a, raw)
	}
	t.Log("compared 8 browser debug success responses")
}
