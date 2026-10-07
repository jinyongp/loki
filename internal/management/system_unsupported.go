//go:build !linux

package management

import (
	"context"
	"fmt"
	"io"
)

func PrepareSystemHost(context.Context, Store, io.Reader, io.Writer) (*ExecutionSelection, error) {
	return nil, fmt.Errorf("system host requires Linux")
}

func (s Store) requireSystemMutable() error { return nil }
