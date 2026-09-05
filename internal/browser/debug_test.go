package browser

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"loki/internal/cdp"
)

func event(d *Debug, method string, params map[string]any) {
	raw, _ := json.Marshal(params)
	d.Event(cdp.Event{Method: method, Params: raw, SessionID: "session"})
}
func TestDebugRedactionAndRetention(t *testing.T) {
	d := &Debug{}
	event(d, "Network.requestWillBeSent", map[string]any{"requestId": "1", "type": "Fetch", "request": map[string]any{"url": "https://example.test/path?token=hidden&q=yes", "method": "GET", "headers": map[string]any{"Authorization": "hidden", "Accept": "text/plain"}}})
	event(d, "Network.responseReceived", map[string]any{"requestId": "1", "response": map[string]any{"status": 500, "mimeType": "text/plain", "headers": map[string]any{"Set-Cookie": "hidden"}}})
	event(d, "Network.loadingFinished", map[string]any{"requestId": "1", "encodedDataLength": 10})
	r := d.Request("1")
	data, _ := json.Marshal(r)
	if strings.Contains(string(data), "hidden") || r["finished"] != true {
		t.Fatal(string(data))
	}
	r["url"] = "mutated"
	if d.Request("1")["url"] == "mutated" {
		t.Fatal("mutable request")
	}
	if len(d.Network(nil, true, "fetch", 0, 100)["requests"].([]map[string]any)) != 1 {
		t.Fatal("failed request filter")
	}
	event(d, "Network.webSocketFrameReceived", map[string]any{"requestId": "ws", "response": map[string]any{"payloadData": "private frame content", "opcode": 1}})
	data, _ = json.Marshal(d.Events("websockets", "", 0, 100))
	if strings.Contains(string(data), "private frame content") {
		t.Fatal("payload exposed")
	}
	for i := 0; i < 1100; i++ {
		event(d, "Runtime.consoleAPICalled", map[string]any{"type": "log", "args": []any{map[string]any{"value": fmt.Sprint(i)}}})
	}
	if d.Events("console", "log", 0, 10)["retained"] != 1000 {
		t.Fatal("console cap")
	}
	for i := 0; i < 2100; i++ {
		event(d, "Network.requestWillBeSent", map[string]any{"requestId": fmt.Sprint(i), "request": map[string]any{}})
	}
	if d.Request("1") != nil || d.Network(nil, false, "", 0, 10)["retained"] != 2000 {
		t.Fatal("request cap")
	}
	d.Reset()
	if d.Events("console", "", 0, 10)["latest_sequence"] != int64(0) {
		t.Fatal("reset")
	}
}
