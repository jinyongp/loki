package main

import "loki/internal/portguard"

type r14Observation struct {
	GoMCPPortProtected     bool `json:"go_mcp_port_protected"`
	LegacyMCPPortProtected bool `json:"legacy_mcp_port_protected"`
}

func observeValidation() r14Observation {
	return r14Observation{
		GoMCPPortProtected:     portguard.Validate(18765) != nil,
		LegacyMCPPortProtected: portguard.Validate(8765) != nil,
	}
}
