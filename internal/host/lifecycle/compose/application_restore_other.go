//go:build !linux

package compose

import (
	"context"
	"loki/internal/host/lifecycle"
)

func (b *Backend) applicationRestoreRecord(context.Context) (lifecycle.OperationRecord, *lifecycle.OperationJournal, error) {
	return lifecycle.OperationRecord{}, nil, errApplicationRestorePlatform
}
