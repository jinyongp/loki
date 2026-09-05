package browser

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"loki/internal/cdp"
)

var sensitive = regexp.MustCompile(`(?i)(?:auth|cookie|token|secret|password|passwd|api[-_]?key|signature|credential)`)

func object(value any) map[string]any {
	m, _ := value.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}
func value(m map[string]any, key string, fallback any) any {
	if v, ok := m[key]; ok {
		return v
	}
	return fallback
}
func text(v any, limit int) string {
	s := ""
	switch v := v.(type) {
	case nil:
		s = "None"
	case bool:
		if v {
			s = "True"
		} else {
			s = "False"
		}
	default:
		s = fmt.Sprint(v)
	}
	runes := []rune(s)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return s
}
func cut(s string, limit int) string {
	r := []rune(s)
	if len(r) > limit {
		return string(r[:limit])
	}
	return s
}
func SanitizeHeaders(v any) map[string]string {
	out := map[string]string{}
	for key, v := range object(v) {
		if len(out) >= 128 {
			break
		}
		name := text(key, 200)
		if sensitive.MatchString(name) {
			out[name] = "[REDACTED]"
		} else {
			out[name] = text(v, 2000)
		}
	}
	return out
}
func SanitizeURL(v any) string {
	s := text(v, 4096)
	u, err := url.Parse(s)
	if err != nil {
		return s
	}
	query := []string{}
	for _, pair := range strings.Split(u.RawQuery, "&") {
		if pair == "" {
			continue
		}
		key, val, _ := strings.Cut(pair, "=")
		key, err = url.QueryUnescape(key)
		if err != nil {
			continue
		}
		val, err = url.QueryUnescape(val)
		if err != nil {
			continue
		}
		if sensitive.MatchString(key) {
			val = "[REDACTED]"
		}
		query = append(query, url.QueryEscape(key)+"="+url.QueryEscape(val))
	}
	u.RawQuery = strings.Join(query, "&")
	if u.User != nil {
		u.User = url.User("[REDACTED]")
	}
	return u.String()
}
func remote(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return text(v, 2000)
	}
	if v, ok := m["value"]; ok {
		return text(v, 2000)
	}
	for _, key := range []string{"unserializableValue", "description", "className", "type"} {
		if v := m[key]; v != nil && v != "" {
			return text(v, 2000)
		}
	}
	return "object"
}
func stack(v any) []map[string]any {
	frames, _ := object(v)["callFrames"].([]any)
	result := []map[string]any{}
	for _, v := range frames[:min(10, len(frames))] {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		result = append(result, map[string]any{"function": text(value(m, "functionName", ""), 300), "url": SanitizeURL(value(m, "url", "")), "line": m["lineNumber"], "column": m["columnNumber"]})
	}
	return result
}
func clone(m map[string]any) map[string]any {
	data, _ := json.Marshal(m)
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	return result
}

type Debug struct {
	mu                                       sync.Mutex
	sequence                                 int64
	console, pageErrors, network, websockets []map[string]any
	requests                                 map[string]map[string]any
	order                                    []string
}

func (d *Debug) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sequence = 0
	d.console = nil
	d.pageErrors = nil
	d.network = nil
	d.websockets = nil
	d.requests = map[string]map[string]any{}
	d.order = nil
}
func (d *Debug) record(target *[]map[string]any, kind, session string, data map[string]any) {
	d.sequence++
	data["sequence"] = d.sequence
	data["captured_at"] = time.Now().UTC().Format("2006-01-02T15:04:05.000+00:00")
	data["kind"] = kind
	data["session_id"] = session
	*target = append(*target, data)
	if len(*target) > 1000 {
		copy(*target, (*target)[1:])
		*target = (*target)[:1000]
	}
}
func (d *Debug) remember(id string, item map[string]any) {
	if d.requests == nil {
		d.requests = map[string]map[string]any{}
	}
	if _, ok := d.requests[id]; !ok {
		d.order = append(d.order, id)
	}
	d.requests[id] = item
	for len(d.order) > 2000 {
		delete(d.requests, d.order[0])
		d.order = d.order[1:]
	}
}
func (d *Debug) Event(e cdp.Event) {
	var m map[string]any
	if json.Unmarshal(e.Params, &m) != nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	id := text(value(m, "requestId", ""), 300)
	item := d.requests[id]
	if item == nil {
		item = map[string]any{"request_id": id, "session_id": e.SessionID}
	}
	switch e.Method {
	case "Runtime.consoleAPICalled":
		args, _ := m["args"].([]any)
		parts := []string{}
		size := 0
		for _, arg := range args {
			part := remote(arg)
			parts = append(parts, part)
			size += len(part)
			if size > 8000 {
				break
			}
		}
		d.record(&d.console, "console", e.SessionID, map[string]any{"level": text(value(m, "type", "log"), 50), "text": cut(strings.Join(parts, " "), 2000), "stack": stack(m["stackTrace"])})
	case "Log.entryAdded":
		entry := object(m["entry"])
		d.record(&d.console, "browser_log", e.SessionID, map[string]any{"level": text(value(entry, "level", "info"), 50), "source": text(value(entry, "source", ""), 100), "text": text(value(entry, "text", ""), 2000), "url": SanitizeURL(value(entry, "url", "")), "line": entry["lineNumber"], "stack": stack(entry["stackTrace"])})
	case "Runtime.exceptionThrown":
		details := object(m["exceptionDetails"])
		exception := object(details["exception"])
		message := value(details, "text", "Unknown exception")
		if v := exception["description"]; v != nil && v != "" {
			message = v
		} else if v := exception["value"]; v != nil {
			message = v
		}
		d.record(&d.pageErrors, "exception", e.SessionID, map[string]any{"text": text(message, 2000), "url": SanitizeURL(value(details, "url", "")), "line": details["lineNumber"], "column": details["columnNumber"], "stack": stack(details["stackTrace"])})
	case "Network.requestWillBeSent":
		if id == "" {
			return
		}
		request := object(m["request"])
		item = map[string]any{"request_id": id, "session_id": e.SessionID, "url": SanitizeURL(value(request, "url", "")), "method": text(value(request, "method", "GET"), 30), "resource_type": text(value(m, "type", "Other"), 50), "request_headers": SanitizeHeaders(request["headers"]), "status": nil, "failed": false, "finished": false, "encoded_bytes": nil, "mime_type": nil}
		d.remember(id, item)
		d.record(&d.network, "request", e.SessionID, map[string]any{"request_id": id, "url": item["url"], "method": item["method"], "resource_type": item["resource_type"]})
	case "Network.responseReceived":
		r := object(m["response"])
		for k, v := range map[string]any{"session_id": e.SessionID, "url": SanitizeURL(value(r, "url", value(item, "url", ""))), "status": r["status"], "status_text": text(value(r, "statusText", ""), 300), "mime_type": text(value(r, "mimeType", ""), 200), "protocol": text(value(r, "protocol", ""), 100), "remote_ip": text(value(r, "remoteIPAddress", ""), 100), "from_disk_cache": r["fromDiskCache"] == true, "response_headers": SanitizeHeaders(r["headers"])} {
			item[k] = v
		}
		d.remember(id, item)
		d.record(&d.network, "response", e.SessionID, map[string]any{"request_id": id, "url": item["url"], "status": item["status"], "mime_type": item["mime_type"], "resource_type": text(value(m, "type", value(item, "resource_type", "Other")), 50)})
	case "Network.loadingFinished":
		item["finished"], item["encoded_bytes"] = true, m["encodedDataLength"]
		d.remember(id, item)
	case "Network.loadingFailed":
		item["failed"], item["finished"], item["error"], item["blocked_reason"], item["canceled"] = true, true, text(value(m, "errorText", "Network request failed"), 2000), text(value(m, "blockedReason", ""), 200), m["canceled"] == true
		d.remember(id, item)
		d.record(&d.network, "failure", e.SessionID, map[string]any{"request_id": id, "url": value(item, "url", ""), "resource_type": text(value(m, "type", value(item, "resource_type", "Other")), 50), "error": item["error"], "blocked_reason": item["blocked_reason"], "canceled": item["canceled"]})
	case "Network.webSocketCreated":
		d.record(&d.websockets, "created", e.SessionID, map[string]any{"request_id": id, "url": SanitizeURL(value(m, "url", ""))})
	case "Network.webSocketWillSendHandshakeRequest", "Network.webSocketHandshakeResponseReceived":
		direction := "request"
		if e.Method == "Network.webSocketHandshakeResponseReceived" {
			direction = "response"
		}
		detail := object(m[direction])
		d.record(&d.websockets, "handshake_"+direction, e.SessionID, map[string]any{"request_id": id, "status": detail["status"], "headers": SanitizeHeaders(detail["headers"])})
	case "Network.webSocketFrameSent", "Network.webSocketFrameReceived":
		direction := "sent"
		if e.Method == "Network.webSocketFrameReceived" {
			direction = "received"
		}
		frame := object(m["response"])
		payload := fmt.Sprint(value(frame, "payloadData", ""))
		d.record(&d.websockets, "frame_"+direction, e.SessionID, map[string]any{"request_id": id, "opcode": frame["opcode"], "masked": frame["mask"] == true, "payload_bytes": len(payload)})
	case "Network.webSocketClosed":
		d.record(&d.websockets, "closed", e.SessionID, map[string]any{"request_id": id})
	case "Network.webSocketFrameError":
		d.record(&d.websockets, "error", e.SessionID, map[string]any{"request_id": id, "error": text(value(m, "errorMessage", "WebSocket error"), 2000)})
	}
}
func page(items []map[string]any, since int64, limit int) []map[string]any {
	out := []map[string]any{}
	for _, item := range items {
		sequence, _ := item["sequence"].(int64)
		if sequence > since {
			out = append(out, clone(item))
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}
func (d *Debug) Events(kind, level string, since int64, limit int) map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	items, key := d.console, "events"
	if kind == "page_errors" {
		items, key = d.pageErrors, "errors"
	}
	if kind == "websockets" {
		items = d.websockets
	}
	out := page(items, since, 1000)
	if level != "" {
		filtered := []map[string]any{}
		for _, item := range out {
			if strings.EqualFold(fmt.Sprint(item["level"]), level) {
				filtered = append(filtered, item)
			}
		}
		out = filtered
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return map[string]any{key: out, "latest_sequence": d.sequence, "retained": len(items)}
}
func failed(item map[string]any) bool {
	status, _ := item["status"].(float64)
	return item["failed"] == true || status >= 400
}
func (d *Debug) Network(statusMin *int, failedOnly bool, resource string, since int64, limit int) map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	ids := map[string]bool{}
	if since > 0 {
		for _, item := range page(d.network, since, 1000) {
			ids[fmt.Sprint(item["request_id"])] = true
		}
	}
	out := []map[string]any{}
	for _, id := range d.order {
		item := d.requests[id]
		status, hasStatus := item["status"].(float64)
		if statusMin != nil && (!hasStatus || status < float64(*statusMin)) || failedOnly && !failed(item) || resource != "" && !strings.EqualFold(fmt.Sprint(item["resource_type"]), resource) || since > 0 && !ids[id] {
			continue
		}
		out = append(out, clone(item))
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return map[string]any{"requests": out, "latest_sequence": d.sequence, "retained": len(d.requests)}
}
func (d *Debug) Request(id string) map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	if item := d.requests[id]; item != nil {
		return clone(item)
	}
	return nil
}
func (d *Debug) Diagnostics(since int64, limit int) map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	failures := []map[string]any{}
	for _, id := range d.order {
		if item := d.requests[id]; failed(item) {
			failures = append(failures, clone(item))
		}
	}
	count := len(failures)
	if count > limit {
		failures = failures[count-limit:]
	}
	return map[string]any{"summary": map[string]any{"console_events": len(d.console), "page_errors": len(d.pageErrors), "network_requests": len(d.requests), "failed_requests": count, "websocket_events": len(d.websockets), "latest_sequence": d.sequence}, "recent_console": page(d.console, since, limit), "recent_page_errors": page(d.pageErrors, since, limit), "recent_failed_requests": failures, "recent_websockets": page(d.websockets, since, limit)}
}
