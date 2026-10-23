//go:build !linux

package compose

import (
	"context"
	"loki/internal/host/lifecycle"
)

func (b *Backend) captureApplicationBackup(context.Context, runtimeState, string) (*lifecycle.ApplicationBackupCheckpoint, error) {
	return nil, errApplicationRestorePlatform
}

func (b *Backend) ConfigureApplicationBackup(context.Context, string) error {
	return errApplicationRestorePlatform
}

func (b *Backend) releaseApplicationBackup(ctx context.Context, _ runtimeState, _ string) error {
	r, _, err := lifecycle.CurrentHostOperation(ctx)
	if err != nil || r.ApplicationBackup == nil {
		return nil
	}
	return errApplicationRestorePlatform
}

func (b *Backend) validateApplicationSafetyRestore(context.Context, runtimeState, *lifecycle.ApplicationBackupCheckpoint) error {
	return errApplicationRestorePlatform
}
