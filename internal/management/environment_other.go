//go:build !linux

package management

import (
	"context"
	"fmt"
	"io"
)

func PrepareEnvironment(context.Context, io.Reader, io.Writer) error {
	return fmt.Errorf("full tools need a Linux execution host; configure WSL or SSH with loki setup")
}
