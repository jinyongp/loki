//go:build !linux

package compose

import "context"

func (b *Backend) restoreLauncherHistory(context.Context, runtimeState, string, string) error {
	return errApplicationRestorePlatform
}
