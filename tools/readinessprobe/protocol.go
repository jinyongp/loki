package main

import (
	"loki/internal/browser"
	"loki/internal/portguard"
)

type r14Observation struct {
	MalformedBrowserProxyPanicked bool `json:"malformed_browser_proxy_panicked"`
	GoMCPPortProtected            bool `json:"go_mcp_port_protected"`
	LegacyMCPPortProtected        bool `json:"legacy_mcp_port_protected"`
}

func observeValidation() r14Observation {
	return r14Observation{
		MalformedBrowserProxyPanicked: browserProxyPanics(),
		GoMCPPortProtected:            portguard.Validate(18765) != nil,
		LegacyMCPPortProtected:        portguard.Validate(8765) != nil,
	}
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
