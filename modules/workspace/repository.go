package workspace

import (
	"context"
	"errors"
	"loki/internal/policy"
	"loki/internal/workcommand"
)

// RepositoryAdapter is the explicit repository dependency for tracked-file
// protection and Git patch application. Ordinary file operations need none.
type RepositoryAdapter interface {
	Paths() *policy.Workspace
	CommandRunner() workcommand.Runner
	TrackedFile(context.Context, string) (bool, error)
}

func (f *Files) AttachRepository(repository RepositoryAdapter) error {
	if f == nil || f.Policy == nil || repository == nil || repository.Paths() != f.Policy || repository.CommandRunner() == nil {
		return errors.New("workspace repository adapter is absent or has different paths")
	}
	f.gitRunner = repository.CommandRunner()
	f.repository = repository
	return nil
}
