package management

import "io"

func NewFullBackend(store Store, diagnostics io.Writer) (FullBackend, error) {
	return NewDockerFullBackend(store, "", diagnostics)
}
