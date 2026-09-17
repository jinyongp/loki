package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/browser"
	"loki/internal/contract"
	"loki/internal/mcpserver"
	"loki/internal/portguard"
)

type r11Observation struct {
	MisspelledCASFieldRejected bool `json:"misspelled_cas_field_rejected"`
}

type r14Observation struct {
	MalformedBrowserProxyPanicked bool `json:"malformed_browser_proxy_panicked"`
	GoMCPPortProtected            bool `json:"go_mcp_port_protected"`
	LegacyMCPPortProtected        bool `json:"legacy_mcp_port_protected"`
}

func observeProtocolAndValidation() (r11Observation, r14Observation, error) {
	ctx := context.Background()
	definitions, err := contract.CurrentDefinitions()
	if err != nil {
		return r11Observation{}, r14Observation{}, err
	}
	handlers := map[string]mcpserver.Handler{}
	for _, definition := range definitions {
		handlers[definition.Name] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
			return mcpserver.Object(input)
		}
	}
	server, err := mcpserver.New(handlers)
	if err != nil {
		return r11Observation{}, r14Observation{}, err
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		return r11Observation{}, r14Observation{}, err
	}
	defer serverSession.Close()
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "synthetic-readiness-probe", Version: "1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		return r11Observation{}, r14Observation{}, err
	}
	defer clientSession.Close()

	result, callErr := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "git_stage",
		Arguments: map[string]any{
			"action":               "paths",
			"paths":                []string{"fixture.txt"},
			"expected_index_sha25": "misspelled-not-a-real-index",
		},
	})
	rejected := callErr != nil || result != nil && result.IsError

	return r11Observation{MisspelledCASFieldRejected: rejected}, r14Observation{
		MalformedBrowserProxyPanicked: browserProxyPanics(),
		GoMCPPortProtected:            portguard.Validate(18765) != nil,
		LegacyMCPPortProtected:        portguard.Validate(8765) != nil,
	}, nil
}

func browserProxyPanics() (panicked bool) {
	defer func() {
		if recover() != nil {
			panicked = true
		}
	}()
	_, _ = browser.NewDriver(browser.Options{
		Binary: "/fixture/chrome", Profile: "/fixture/profile", Downloads: "/fixture/downloads", Proxy: "http://%",
	})
	return false
}
