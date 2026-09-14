package devtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
)

type Version struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

func ParseVersion(raw []byte) (Version, error) {
	var envelope Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return Version{}, fmt.Errorf("decode devtools version envelope: %w", err)
	}
	if envelope.SchemaVersion != ProtocolVersion || !envelope.OK || len(envelope.Error) != 0 {
		return Version{}, errors.New("unsupported devtools version envelope")
	}
	var version Version
	if err := json.Unmarshal(envelope.Data, &version); err != nil {
		return Version{}, fmt.Errorf("decode devtools version: %w", err)
	}
	if version.Version != SupportedVersion {
		return Version{}, fmt.Errorf("unsupported devtools version %q; require %s", version.Version, SupportedVersion)
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
