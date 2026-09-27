package windows

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type publicConnectionFile struct {
	SchemaVersion int `json:"schema_version"`
	LocalOrigin   struct {
		URL            string `json:"url"`
		Transport      string `json:"transport"`
		Reachability   string `json:"reachability"`
		Authentication struct {
			Type      string `json:"type"`
			TokenFile string `json:"token_file"`
		} `json:"authentication"`
	} `json:"local_origin"`
	Endpoint       string `json:"endpoint"`
	Transport      string `json:"transport"`
	Authentication string `json:"authentication"`
	TokenFile      string `json:"token_file"`
	Distribution   string `json:"distribution"`
}

func BuildPublicConnection(expected ExpectedInstallation, material ConnectionMaterial) ([]byte, error) {
	if material.Transport != "streamable-http" ||
		material.Reachability != "loopback" ||
		material.AuthenticationType != "bearer-token-file" ||
		material.Token == "" || strings.ContainsAny(material.Token, "\r\n\x00") {
		return nil, errors.New("connection material does not match Windows installer contract")
	}
	origin, _, err := parseLoopbackOrigin(material.LocalOrigin, false)
	if err != nil {
		return nil, err
	}
	tokenFile := joinWindowsPath(expected.StateDir, "mcp-token")
	var payload publicConnectionFile
	payload.SchemaVersion = 1
	payload.LocalOrigin.URL = origin
	payload.LocalOrigin.Transport = material.Transport
	payload.LocalOrigin.Reachability = material.Reachability
	payload.LocalOrigin.Authentication.Type = material.AuthenticationType
	payload.LocalOrigin.Authentication.TokenFile = tokenFile
	payload.Endpoint = origin
	payload.Transport = material.Transport
	payload.Authentication = "bearer-token-file"
	payload.TokenFile = tokenFile
	payload.Distribution = expected.Distribution
	return json.MarshalIndent(payload, "", "  ")
}

func BuildOwnershipManifest(binding ReleaseBinding, expected ExpectedInstallation, options InstallOptions) ([]byte, error) {
	if binding.ReleaseTag == "" {
		return nil, errors.New("Windows frontend release binding has no release tag")
	}
	manifest := ownershipManifestDisk{
		SchemaVersion:   1,
		Distribution:    expected.Distribution,
		ReleaseTag:      binding.ReleaseTag,
		StateDir:        expected.StateDir,
		InstallLocation: options.InstallLocation,
		AutoStart:       options.AutoStart,
		MCPPort:         options.MCPPort,
		TaskName:        expected.TaskName,
		TaskExecutable:  expected.TaskExecutable,
		TaskArguments:   expected.TaskArguments,
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if _, err = ParseOwnershipManifest(raw, expected); err != nil {
		return nil, fmt.Errorf("generated ownership manifest is invalid: %w", err)
	}
	return raw, nil
}
