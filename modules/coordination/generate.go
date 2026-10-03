package devtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
)

var semanticVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

type Version struct {
	Version         string `json:"version"`
	Commit          string `json:"commit"`
	ProtocolVersion int    `json:"protocol_version"`
}

func ParseVersion(raw []byte) (Version, error) {
	data, err := successData(raw)
	if err != nil {
		return Version{}, err
	}
	var version Version
	if err := decodeObject(data, &version); err != nil {
		return Version{}, errors.New("invalid devtools version response")
	}
	if version.ProtocolVersion != ProtocolVersion {
		return Version{}, fmt.Errorf("unsupported devtools protocol version %d", version.ProtocolVersion)
	}
	if !semanticVersion.MatchString(version.Version) {
		return Version{}, errors.New("invalid devtools release version")
	}
	return version, nil
}

func GenerateCatalog(ctx context.Context, binary string) ([]byte, error) {
	if binary == "" {
		return nil, errors.New("devtools binary path is required")
	}
	version, err := exec.CommandContext(ctx, binary, "version").Output()
	if err != nil {
		return nil, fmt.Errorf("run devtools version: %w", err)
	}
	if _, err = ParseVersion(version); err != nil {
		return nil, err
	}
	raw, err := exec.CommandContext(ctx, binary, "schema", "--all").Output()
	if err != nil {
		return nil, fmt.Errorf("run devtools schema --all: %w", err)
	}
	if _, err = ParseCatalog(raw); err != nil {
		return nil, err
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err = decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("normalize devtools catalog: %w", err)
	}
	normalized, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("normalize devtools catalog: %w", err)
	}
	return append(normalized, '\n'), nil
}
