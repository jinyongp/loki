package windows

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	connectionStateFileName         = "connection.json"
	connectionTaskOwnershipFileName = "startup-task.json"
)

func ConnectionProviderRoot(paths FrontendPaths, distribution, provider string) (string, error) {
	if err := ValidateDistributionName(distribution); err != nil {
		return "", err
	}
	if !connectionProviderPattern.MatchString(provider) {
		return "", errors.New("managed connection provider name is invalid")
	}
	if paths.ConnectionsRoot == "" {
		return "", errors.New("managed connection root is empty")
	}
	return joinWindowsPath(paths.ConnectionsRoot, distribution+"\\"+provider), nil
}

func encodeConnectionState(state ConnectionState) ([]byte, error) {
	if err := validateConnectionState(state); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func parseConnectionState(raw []byte) (ConnectionState, error) {
	var state ConnectionState
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return ConnectionState{}, fmt.Errorf("decode managed connection state: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return ConnectionState{}, err
	}
	if err := validateConnectionState(state); err != nil {
		return ConnectionState{}, err
	}
	return state, nil
}
