// Package contract records and loads the observable compatibility contract.
package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
)

// Snapshot retains raw JSON so SDK defaults cannot silently change a fixture.
type Snapshot struct {
	Baseline          string                     `json:"baseline"`
	Initialize        json.RawMessage            `json:"initialize"`
	Tools             []json.RawMessage          `json:"tools"`
	Resources         []json.RawMessage          `json:"resources"`
	ResourceTemplates []json.RawMessage          `json:"resourceTemplates"`
	ResourceContents  map[string]json.RawMessage `json:"resourceContents"`
	InvalidCalls      []CallFixture              `json:"invalidCalls,omitempty"`
}

type CallFixture struct {
	Tool      string          `json:"tool"`
	Arguments map[string]any  `json:"arguments"`
	Result    json.RawMessage `json:"result"`
}

// Capture reads discovery and static widgets. Optional invalid-input probes use
// object values where the schema requires a scalar, so validation rejects them
// before domain code executes. Valid tool calls and private state are excluded.
func Capture(ctx context.Context, endpoint, token string, invalidInputs bool) (*Snapshot, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") {
		return nil, errors.New("contract capture requires a loopback HTTP endpoint")
	}
	if len(token) < 43 || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("invalid bearer token")
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	id := 0
	sessionID := ""
	call := func(method string, params any, notify bool) (json.RawMessage, error) {
		id++
		body := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
		if !notify {
			body["id"] = id
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-11-25")
		if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, errors.New("MCP capture connection failed")
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("MCP %s returned HTTP %d", method, resp.StatusCode)
		}
		if value := resp.Header.Get("Mcp-Session-Id"); value != "" {
			sessionID = value
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 16_777_217))
		if err != nil {
			return nil, errors.New("MCP response read failed")
		}
		if len(data) > 16_777_216 {
			return nil, errors.New("MCP capture response exceeds limit")
		}
		if bytes.Contains(data, []byte(token)) {
			return nil, errors.New("credential material in MCP response")
		}
		if notify {
			return nil, nil
		}
		var result struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, errors.New("MCP capture requires JSON response")
		}
		if len(result.Error) > 0 && string(result.Error) != "null" {
			return nil, fmt.Errorf("MCP %s failed", method)
		}
		if len(result.Result) == 0 {
			return nil, errors.New("MCP response missing result")
		}
		return result.Result, nil
	}
	initialized, err := call("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "loki-contract-capture", "version": "1"}}, false)
	if err != nil {
		return nil, err
	}
	var init struct {
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(initialized, &init); err != nil {
		return nil, err
	}
	if init.ServerInfo.Name != "loki" || init.ServerInfo.Version != "0.47.1" {
		return nil, fmt.Errorf("expected loki 0.47.1 baseline, got %s %s", init.ServerInfo.Name, init.ServerInfo.Version)
	}
	if _, err = call("notifications/initialized", map[string]any{}, true); err != nil {
		return nil, err
	}
	s := &Snapshot{Baseline: "python-0.47.1", Initialize: initialized, ResourceContents: map[string]json.RawMessage{}}
	for _, entry := range []struct {
		method, key string
		target      *[]json.RawMessage
	}{{"tools/list", "tools", &s.Tools}, {"resources/list", "resources", &s.Resources}, {"resources/templates/list", "resourceTemplates", &s.ResourceTemplates}} {
		cursor := ""
		seen := map[string]bool{}
		for {
			params := map[string]any{}
			if cursor != "" {
				params["cursor"] = cursor
			}
			data, err := call(entry.method, params, false)
			if err != nil {
				return nil, err
			}
			var result map[string]json.RawMessage
			if err = json.Unmarshal(data, &result); err != nil {
				return nil, err
			}
			var items []json.RawMessage
			if err = json.Unmarshal(result[entry.key], &items); err != nil {
				return nil, err
			}
			*entry.target = append(*entry.target, items...)
			cursor = ""
			if raw := result["nextCursor"]; len(raw) > 0 {
				if err = json.Unmarshal(raw, &cursor); err != nil {
					return nil, err
				}
			}
			if cursor == "" {
				break
			}
			if seen[cursor] {
				return nil, errors.New("repeated MCP discovery cursor")
			}
			seen[cursor] = true
		}
		if *entry.target == nil {
			*entry.target = []json.RawMessage{}
		}
	}
	if len(s.Tools) != 37 {
		return nil, fmt.Errorf("baseline requires 37 tools, got %d", len(s.Tools))
	}
	for _, resource := range s.Resources {
		var r struct {
			URI string `json:"uri"`
		}
		if err = json.Unmarshal(resource, &r); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(r.URI, "ui://") {
			return nil, errors.New("capture only permits static ui resources")
		}
		contents, err := call("resources/read", map[string]string{"uri": r.URI}, false)
		if err != nil {
			return nil, err
		}
		s.ResourceContents[r.URI] = contents
	}
	if invalidInputs {
		for _, raw := range s.Tools {
			var tool struct {
				Name   string            `json:"name"`
				Schema jsonschema.Schema `json:"inputSchema"`
			}
			if err = json.Unmarshal(raw, &tool); err != nil {
				return nil, err
			}
			keys := []string{}
			for key, property := range tool.Schema.Properties {
				if property.Type == "string" || property.Type == "boolean" || property.Type == "integer" {
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			if len(keys) == 0 {
				return nil, fmt.Errorf("no safe invalid-input probe for %s", tool.Name)
			}
			args := map[string]any{keys[0]: map[string]any{}}
			resolved, err := tool.Schema.Resolve(nil)
			if err != nil {
				return nil, err
			}
			if resolved.Validate(args) == nil {
				return nil, errors.New("invalid-input probe unexpectedly validates")
			}
			result, err := call("tools/call", map[string]any{"name": tool.Name, "arguments": args}, false)
			if err != nil {
				return nil, err
			}
			var response struct {
				IsError bool `json:"isError"`
			}
			if json.Unmarshal(result, &response) != nil || !response.IsError {
				return nil, fmt.Errorf("%s invalid-input probe was not rejected", tool.Name)
			}
			s.InvalidCalls = append(s.InvalidCalls, CallFixture{tool.Name, args, result})
		}
	}
	return s, nil
}
