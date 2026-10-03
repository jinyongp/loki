//go:build !linux

package management

import (
	"fmt"
	"io"
)

func NewFullBackend(Store, io.Writer) (FullBackend, error) {
	return nil, fmt.Errorf("full services require a Linux execution host; select an existing WSL or SSH host")
}
