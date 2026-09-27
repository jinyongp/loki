//go:build windows

package windows

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type WindowsConnectionStateStore struct {
	LocalAppData string
	Platform     WindowsFrontendPlatform
}

func NewWindowsConnectionStateStore(localAppData string) WindowsConnectionStateStore {
	return WindowsConnectionStateStore{
		LocalAppData: localAppData,
		Platform:     NewWindowsFrontendPlatform(),
	}
}

func (store WindowsConnectionStateStore) frontendPaths() (FrontendPaths, error) {
	root := strings.TrimSpace(store.LocalAppData)
	if root == "" {
		root = strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	}
	return ResolveFrontendPaths(root)
}

func (store WindowsConnectionStateStore) ProviderRoot(distribution, provider string) (string, error) {
	if err := ValidateDistributionName(distribution); err != nil {
		return "", err
	}
	if !connectionProviderPattern.MatchString(provider) {
		return "", errors.New("managed connection provider name is invalid")
	}
	paths, err := store.frontendPaths()
	if err != nil {
		return "", err
	}
	return joinWindowsPath(paths.ConnectionsRoot, distribution+"\\"+provider), nil
}

func (store WindowsConnectionStateStore) distributionRoot(distribution string) (string, error) {
	if err := ValidateDistributionName(distribution); err != nil {
		return "", err
	}
	paths, err := store.frontendPaths()
	if err != nil {
		return "", err
	}
	return joinWindowsPath(paths.ConnectionsRoot, distribution), nil
}

func (store WindowsConnectionStateStore) EnsureDistributionRoot(ctx context.Context, distribution string) error {
	paths, err := store.frontendPaths()
	if err != nil {
		return err
	}
	distributionRoot, err := store.distributionRoot(distribution)
	if err != nil {
		return err
	}
	platform := WindowsHelperInstallPlatform{}
	for _, directory := range []string{paths.ConnectionsRoot, distributionRoot} {
		if err = platform.EnsurePrivateDirectory(ctx, directory); err != nil {
			return fmt.Errorf("prepare managed connection directory %s: %w", directory, err)
		}
		if err = platform.VerifyPrivatePath(directory, true); err != nil {
			return fmt.Errorf("verify managed connection directory %s: %w", directory, err)
		}
	}
	return nil
}

func (store WindowsConnectionStateStore) EnsureProviderRoot(ctx context.Context, distribution, provider string) error {
	root, err := store.ProviderRoot(distribution, provider)
	if err != nil {
		return err
	}
	if err = store.EnsureDistributionRoot(ctx, distribution); err != nil {
		return err
	}
	platform := WindowsHelperInstallPlatform{}
	if err = platform.EnsurePrivateDirectory(ctx, root); err != nil {
		return fmt.Errorf("prepare managed connection directory %s: %w", root, err)
	}
	if err = platform.VerifyPrivatePath(root, true); err != nil {
		return fmt.Errorf("verify managed connection directory %s: %w", root, err)
	}
	return nil
}

func (store WindowsConnectionStateStore) Read(distribution, provider string) (ConnectionState, bool, error) {
	root, err := store.ProviderRoot(distribution, provider)
	if err != nil {
		return ConnectionState{}, false, err
	}
	info, err := OSStateFilesystem{}.Lstat(root)
	if err != nil {
		return ConnectionState{}, false, err
	}
	if !info.Exists {
		return ConnectionState{}, false, nil
	}
	if !info.Directory || info.Reparse {
		return ConnectionState{}, false, errors.New("managed connection provider root is not a real directory")
	}
	if err = verifyPrivateACL(root, true); err != nil {
		return ConnectionState{}, false, fmt.Errorf("managed connection provider ACL drift: %w", err)
	}
	statePath := joinWindowsPath(root, connectionStateFileName)
	stateInfo, err := OSStateFilesystem{}.Lstat(statePath)
	if err != nil {
		return ConnectionState{}, false, err
	}
	if !stateInfo.Exists {
		return ConnectionState{}, false, errors.New("managed connection provider root exists without Loki state ownership")
	}
	if !stateInfo.Regular || stateInfo.Reparse {
		return ConnectionState{}, false, errors.New("managed connection state is not a regular non-reparse file")
	}
	if err = verifyPrivateACL(statePath, false); err != nil {
		return ConnectionState{}, false, fmt.Errorf("managed connection state ACL drift: %w", err)
	}
	raw, err := os.ReadFile(statePath)
	if err != nil {
		return ConnectionState{}, false, err
	}
	state, err := parseConnectionState(raw)
	if err != nil {
		return ConnectionState{}, false, err
	}
	if state.Distribution != distribution || state.Provider != provider {
		return ConnectionState{}, false, errors.New("managed connection state does not match its canonical provider path")
	}
	return state, true, nil
}

func (store WindowsConnectionStateStore) Write(ctx context.Context, state ConnectionState) error {
	if err := validateConnectionState(state); err != nil {
		return err
	}
	if err := store.EnsureProviderRoot(ctx, state.Distribution, state.Provider); err != nil {
		return err
	}
	root, err := store.ProviderRoot(state.Distribution, state.Provider)
	if err != nil {
		return err
	}
	raw, err := encodeConnectionState(state)
	if err != nil {
		return err
	}
	statePath := joinWindowsPath(root, connectionStateFileName)
	platform := store.Platform
	if platform.Runner == nil {
		platform = NewWindowsFrontendPlatform()
	}
	if err = platform.WriteProtectedAtomic(ctx, statePath, raw); err != nil {
		return fmt.Errorf("publish managed connection state: %w", err)
	}
	if err = verifyPrivateACL(statePath, false); err != nil {
		return fmt.Errorf("verify managed connection state ACL: %w", err)
	}
	return nil
}

func (store WindowsConnectionStateStore) List(distribution string) ([]ConnectionState, error) {
	root, err := store.distributionRoot(distribution)
	if err != nil {
		return nil, err
	}
	info, err := OSStateFilesystem{}.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.Exists {
		return nil, nil
	}
	if !info.Directory || info.Reparse {
		return nil, errors.New("managed connection distribution root is not a real directory")
	}
	if err = verifyPrivateACL(root, true); err != nil {
		return nil, fmt.Errorf("managed connection distribution ACL drift: %w", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	states := make([]ConnectionState, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if name == connectionTaskOwnershipFileName {
			info, statErr := OSStateFilesystem{}.Lstat(joinWindowsPath(root, name))
			if statErr != nil {
				return nil, statErr
			}
			if !info.Exists || !info.Regular || info.Reparse {
				return nil, errors.New("managed connection startup ownership path is unsafe")
			}
			continue
		}
		if !connectionProviderPattern.MatchString(name) {
			return nil, fmt.Errorf("managed connection root contains unexpected entry %q", name)
		}
		entryInfo, statErr := OSStateFilesystem{}.Lstat(joinWindowsPath(root, name))
		if statErr != nil {
			return nil, statErr
		}
		if !entryInfo.Exists || !entryInfo.Directory || entryInfo.Reparse {
			return nil, fmt.Errorf("managed connection provider entry %q is unsafe", name)
		}
		state, present, readErr := store.Read(distribution, name)
		if readErr != nil {
			return nil, readErr
		}
		if !present {
			return nil, fmt.Errorf("managed connection provider %q has no owned state", name)
		}
		states = append(states, state)
	}
	slices.SortFunc(states, func(left, right ConnectionState) int {
		return strings.Compare(left.Provider, right.Provider)
	})
	return states, nil
}

func (store WindowsConnectionStateStore) Remove(ctx context.Context, distribution, provider string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := store.ProviderRoot(distribution, provider)
	if err != nil {
		return err
	}
	info, err := OSStateFilesystem{}.Lstat(root)
	if err != nil {
		return err
	}
	if !info.Exists {
		return nil
	}
	if !info.Directory || info.Reparse {
		return errors.New("refusing to remove unsafe managed connection provider root")
	}
	if err = verifyPrivateACL(root, true); err != nil {
		return fmt.Errorf("refusing to remove provider root with ACL drift: %w", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	stateSeen := false
	for _, entry := range entries {
		if entry.Name() != connectionStateFileName {
			return fmt.Errorf("refusing to remove provider root with remaining adapter state %q", entry.Name())
		}
		stateSeen = true
	}
	if stateSeen {
		state, present, readErr := store.Read(distribution, provider)
		if readErr != nil {
			return readErr
		}
		if !present || state.Distribution != distribution || state.Provider != provider {
			return errors.New("refusing to remove unverified managed connection state")
		}
		statePath := joinWindowsPath(root, connectionStateFileName)
		if err = os.Remove(statePath); err != nil {
			return err
		}
	}
	if err = os.Remove(root); err != nil {
		return err
	}
	return store.removeEmptyDistributionRoot(distribution)
}

func (store WindowsConnectionStateStore) removeEmptyDistributionRoot(distribution string) error {
	root, err := store.distributionRoot(distribution)
	if err != nil {
		return err
	}
	info, err := OSStateFilesystem{}.Lstat(root)
	if err != nil {
		return err
	}
	if !info.Exists {
		return nil
	}
	if !info.Directory || info.Reparse {
		return errors.New("managed connection distribution root is unsafe")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return nil
	}
	if err = verifyPrivateACL(root, true); err != nil {
		return err
	}
	return os.Remove(root)
}

func (store WindowsConnectionStateStore) statePath(distribution, provider string) (string, error) {
	root, err := store.ProviderRoot(distribution, provider)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, connectionStateFileName), nil
}
