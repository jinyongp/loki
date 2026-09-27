package windows

import (
	"fmt"
	"sort"
)

type StatePath struct {
	Exists    bool
	Directory bool
	Regular   bool
	Reparse   bool
}

type StateFilesystem interface {
	Lstat(string) (StatePath, error)
	ReadDir(string) ([]string, error)
	ReadFile(string) ([]byte, error)
}

func InspectWindowsState(filesystem StateFilesystem, expected ExpectedInstallation) (WindowsState, error) {
	statePath, err := filesystem.Lstat(expected.StateDir)
	if err != nil {
		return WindowsState{}, fmt.Errorf("inspect Windows state directory: %w", err)
	}
	if !statePath.Exists {
		return WindowsState{Kind: WindowsStateAbsent}, nil
	}
	if !statePath.Directory || statePath.Reparse {
		return unverifiedWindowsState(), nil
	}
	entries, err := filesystem.ReadDir(expected.StateDir)
	if err != nil {
		return WindowsState{}, fmt.Errorf("read Windows state directory: %w", err)
	}
	sort.Strings(entries)

	ownershipPath := joinWindowsPath(expected.StateDir, "ownership.json")
	ownershipInfo, err := filesystem.Lstat(ownershipPath)
	if err != nil {
		return WindowsState{}, fmt.Errorf("inspect Windows ownership manifest: %w", err)
	}
	if ownershipInfo.Exists && ownershipInfo.Regular && !ownershipInfo.Reparse {
		if !sameNames(entries, []string{"connection.json", "mcp-token", "ownership.json"}) {
			return unverifiedWindowsState(), nil
		}
		raw, readErr := filesystem.ReadFile(ownershipPath)
		if readErr != nil {
			return WindowsState{}, fmt.Errorf("read Windows ownership manifest: %w", readErr)
		}
		state, parseErr := ParseOwnershipManifest(raw, expected)
		if parseErr != nil {
			return unverifiedWindowsState(), nil
		}
		if state.InstallLocation != "" {
			locationInfo, statErr := filesystem.Lstat(state.InstallLocation)
			if statErr != nil {
				return WindowsState{}, fmt.Errorf("inspect manifest-owned WSL location: %w", statErr)
			}
			if locationInfo.Exists && (!locationInfo.Directory || locationInfo.Reparse) {
				return unverifiedWindowsState(), nil
			}
		}
		return state, nil
	}

	if !sameNames(entries, []string{"connection.json", "mcp-token"}) {
		return unverifiedWindowsState(), nil
	}
	connectionPath := joinWindowsPath(expected.StateDir, "connection.json")
	tokenPath := joinWindowsPath(expected.StateDir, "mcp-token")
	connectionInfo, err := filesystem.Lstat(connectionPath)
	if err != nil {
		return WindowsState{}, fmt.Errorf("inspect legacy connection file: %w", err)
	}
	tokenInfo, err := filesystem.Lstat(tokenPath)
	if err != nil {
		return WindowsState{}, fmt.Errorf("inspect legacy token file: %w", err)
	}
	if !connectionInfo.Exists || !connectionInfo.Regular || connectionInfo.Reparse ||
		!tokenInfo.Exists || !tokenInfo.Regular || tokenInfo.Reparse {
		return unverifiedWindowsState(), nil
	}
	raw, err := filesystem.ReadFile(connectionPath)
	if err != nil {
		return WindowsState{}, fmt.Errorf("read legacy connection file: %w", err)
	}
	state, err := ParseLegacyConnection(raw, expected)
	if err != nil {
		return unverifiedWindowsState(), nil
	}
	return state, nil
}

func sameNames(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	left := append([]string(nil), actual...)
	right := append([]string(nil), expected...)
	sort.Strings(left)
	sort.Strings(right)
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func unverifiedWindowsState() WindowsState {
	return WindowsState{Present: true, Owned: false, Kind: WindowsStateUnverified}
}
