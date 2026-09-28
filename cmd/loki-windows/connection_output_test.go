package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	windowshost "loki/internal/host/windows"
)

func TestBuildConnectionListIncludesLocalAndManagedCatalog(t *testing.T) {
	descriptors := []windowshost.ConnectionProviderDescriptor{
		{
			ID:          "zeta",
			Kind:        windowshost.ManagedConnectionKind,
			DisplayName: "Zeta",
			Description: "Zeta managed connection.",
			Actions:     []string{"setup", "start", "stop", "remove"},
		},
		{
			ID:          "openai",
			Kind:        windowshost.ManagedConnectionKind,
			DisplayName: "OpenAI Secure MCP Tunnel",
			Description: "OpenAI managed connection.",
			Actions:     []string{"setup", "start", "stop", "remove"},
		},
	}
	states := []windowshost.ConnectionState{
		{SchemaVersion: 1, Distribution: "loki-mcp", Provider: "openai", Enabled: true, HelperID: "helper", HelperVersion: "1", HelperPlatform: "windows-amd64"},
	}

	items, err := buildConnectionList(true, descriptors, states)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("items=%#v", items)
	}
	if items[0].ID != "local" || items[0].State != "available" || !items[0].Configured || !items[0].Enabled {
		t.Fatalf("local item=%#v", items[0])
	}
	if items[1].ID != "openai" || items[1].State != "enabled" || !items[1].Configured || !items[1].Enabled {
		t.Fatalf("openai item=%#v", items[1])
	}
	if items[2].ID != "zeta" || items[2].State != "not-configured" || items[2].Configured || items[2].Enabled {
		t.Fatalf("zeta item=%#v", items[2])
	}
}

func TestBuildConnectionListMarksLocalUnavailableWithoutReplica(t *testing.T) {
	items, err := buildConnectionList(false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "local" || items[0].State != "unavailable" || items[0].Configured {
		t.Fatalf("items=%#v", items)
	}
}

func TestConnectionListHumanOutputIsDiscoverable(t *testing.T) {
	items := []connectionListItem{
		{ID: "local", Kind: "local", State: "available", DisplayName: "Local MCP"},
		{ID: "openai", Kind: "managed", State: "not-configured", DisplayName: "OpenAI Secure MCP Tunnel"},
	}
	var out bytes.Buffer
	renderConnectionList("loki-mcp", items, &out)
	got := out.String()
	for _, want := range []string{
		"Loki connections (loki-mcp)",
		"local",
		"Local MCP",
		"openai",
		"OpenAI Secure MCP Tunnel",
		"loki connection show NAME",
		"loki connection setup PROVIDER",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q: %s", want, got)
		}
	}
}

func TestConnectionListJSONHasSingleConnectionCollection(t *testing.T) {
	items := []connectionListItem{
		{ID: "local", Kind: "local", State: "available", DisplayName: "Local MCP", Configured: true, Enabled: true},
		{ID: "openai", Kind: "managed", State: "stopped", DisplayName: "OpenAI", Configured: true},
	}
	var out bytes.Buffer
	if err := writeConnectionListJSON("loki-mcp", items, &out); err != nil {
		t.Fatal(err)
	}
	var payload connectionListPayload
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SchemaVersion != 1 || payload.Distribution != "loki-mcp" || len(payload.Connections) != 2 {
		t.Fatalf("payload=%#v", payload)
	}
}
