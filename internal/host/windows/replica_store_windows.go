//go:build windows

package windows

import (
	"context"
	"errors"
	"fmt"
	"os"
)

type WindowsReplicaStore struct {
	Platform WindowsFrontendPlatform
}

func NewWindowsReplicaStore() WindowsReplicaStore {
	return WindowsReplicaStore{Platform: NewWindowsFrontendPlatform()}
}

func (store WindowsReplicaStore) Read(expected ExpectedInstallation) (ReplicaSnapshot, error) {
	filesystem := OSStateFilesystem{}
	state, err := InspectWindowsState(filesystem, expected)
	if err != nil {
		return ReplicaSnapshot{}, err
	}
	if !state.Present {
		return ReplicaSnapshot{}, nil
	}
	if !state.Owned {
		return ReplicaSnapshot{}, errors.New("refusing to read unverified Windows connection replica")
	}
	connectionPath := joinWindowsPath(expected.StateDir, "connection.json")
	tokenPath := joinWindowsPath(expected.StateDir, "mcp-token")
	connectionInfo, err := filesystem.Lstat(connectionPath)
	if err != nil {
		return ReplicaSnapshot{}, err
	}
	tokenInfo, err := filesystem.Lstat(tokenPath)
	if err != nil {
		return ReplicaSnapshot{}, err
	}
	if !connectionInfo.Exists || !connectionInfo.Regular || connectionInfo.Reparse ||
		!tokenInfo.Exists || !tokenInfo.Regular || tokenInfo.Reparse {
		return ReplicaSnapshot{}, errors.New("Windows connection replica files are not regular non-reparse files")
	}
	connectionRaw, err := os.ReadFile(connectionPath)
	if err != nil {
		return ReplicaSnapshot{}, err
	}
	tokenRaw, err := os.ReadFile(tokenPath)
	if err != nil {
		return ReplicaSnapshot{}, err
	}
	return BuildReplicaSnapshot(expected, state, connectionRaw, tokenRaw)
}

func (store WindowsReplicaStore) Publish(
	ctx context.Context,
	expected ExpectedInstallation,
	material ConnectionMaterial,
) error {
	filesystem := OSStateFilesystem{}
	state, err := InspectWindowsState(filesystem, expected)
	if err != nil {
		return err
	}
	if !state.Present || !state.Owned {
		return errors.New("refusing to refresh an unverified Windows connection replica")
	}
	_, livePort, err := parseLoopbackOrigin(material.LocalOrigin, false)
	if err != nil {
		return err
	}
	if state.MCPPort > 0 && state.MCPPort != livePort {
		return errors.New("live Loki connection port does not match verified Windows ownership")
	}
	raw, err := BuildPublicConnection(expected, material)
	if err != nil {
		return err
	}
	tokenPath := joinWindowsPath(expected.StateDir, "mcp-token")
	connectionPath := joinWindowsPath(expected.StateDir, "connection.json")
	connectionInfo, err := filesystem.Lstat(connectionPath)
	if err != nil {
		return err
	}
	tokenInfo, err := filesystem.Lstat(tokenPath)
	if err != nil {
		return err
	}
	if !connectionInfo.Exists || !connectionInfo.Regular || connectionInfo.Reparse ||
		!tokenInfo.Exists || !tokenInfo.Regular || tokenInfo.Reparse {
		return errors.New("refusing to refresh Windows connection replica files after type or reparse change")
	}
	oldToken, err := os.ReadFile(tokenPath)
	if err != nil {
		return fmt.Errorf("read current Windows MCP token replica: %w", err)
	}
	oldConnection, err := os.ReadFile(connectionPath)
	if err != nil {
		return fmt.Errorf("read current Windows connection replica: %w", err)
	}
	if err = store.Platform.WriteProtectedAtomic(ctx, tokenPath, []byte(material.Token)); err != nil {
		return fmt.Errorf("refresh Windows MCP token replica: %w", err)
	}
	if err = store.Platform.WriteProtectedAtomic(ctx, connectionPath, raw); err != nil {
		tokenRollback := store.Platform.WriteProtectedAtomic(ctx, tokenPath, oldToken)
		connectionRollback := store.Platform.WriteProtectedAtomic(ctx, connectionPath, oldConnection)
		return errors.Join(
			fmt.Errorf("refresh Windows connection replica: %w", err),
			wrapRollbackError("restore previous Windows MCP token replica", tokenRollback),
			wrapRollbackError("restore previous Windows connection replica", connectionRollback),
		)
	}
	return nil
}

func wrapRollbackError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
