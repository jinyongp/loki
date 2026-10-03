package gitops

import (
	"context"
	"errors"
	"path/filepath"

	"loki/internal/config"
	"loki/internal/policy"
	"loki/modules/execution/jobs"
)

type Repository struct {
	controller *Controller
	runner     Runner
}

// SetBinary binds the repository adapter to its module-owned executable before
// public registration. Paths are administrator layout inputs, never tool input.
func (r *Repository) SetBinary(path string) error {
	if r == nil || r.controller == nil || !filepath.IsAbs(path) {
		return errors.New("repository requires an absolute owned Git executable")
	}
	r.controller.Binary = filepath.Clean(path)
	return nil
}

func OpenRepository(
	paths *policy.Workspace,
	configuration config.Config,
	environment []string,
	templateRoots []*policy.Workspace,
) (*Repository, error) {
	if paths == nil {
		return nil, errors.New("workspace repository requires paths")
	}
	controller := &Controller{
		Paths: paths, Config: configuration,
		Env: append([]string(nil), environment...), TemplateRoots: append([]*policy.Workspace(nil), templateRoots...),
	}
	return &Repository{controller: controller}, nil
}

func NewRepository(
	paths *policy.Workspace,
	configuration config.Config,
	runner jobs.Runner,
	environment []string,
	templateRoots []*policy.Workspace,
) (*Repository, error) {
	repository, err := OpenRepository(paths, configuration, environment, templateRoots)
	if err != nil {
		return nil, err
	}
	if err = repository.BindJobs(runner); err != nil {
		return nil, err
	}
	return repository, nil
}

func (r *Repository) BindJobs(runner jobs.Runner) error {
	if r == nil || r.controller == nil || runner == nil {
		return errors.New("workspace repository requires a Job runner")
	}
	if r.runner != nil {
		return errors.New("workspace repository Job runner is already configured")
	}
	commandRunner := JobRunner{Jobs: runner}
	r.runner = commandRunner
	r.controller.Runner = commandRunner
	return nil
}

func (r *Repository) Paths() *policy.Workspace {
	if r == nil || r.controller == nil {
		return nil
	}
	return r.controller.Paths
}

func (r *Repository) CommandRunner() Runner {
	if r == nil {
		return nil
	}
	return r.runner
}

func (r *Repository) BindRunner(runner Runner) error {
	if r == nil || r.controller == nil || runner == nil || r.runner != nil {
		return errors.New("Git runner is absent or already bound")
	}
	r.runner = runner
	r.controller.Runner = runner
	return nil
}

func (r *Repository) RepositoryRoot(ctx context.Context, cwd string) (string, error) {
	if r == nil || r.controller == nil {
		return "", errors.New("workspace repository is not configured")
	}
	return r.controller.RepositoryRoot(ctx, cwd)
}

func (r *Repository) TrackedFile(ctx context.Context, path string) (bool, error) {
	if r == nil || r.controller == nil {
		return false, errors.New("workspace repository is not configured")
	}
	return r.controller.TrackedFile(ctx, path)
}

func (r *Repository) ContextEvidence(ctx context.Context, cwd, target string) (ContextEvidence, error) {
	if r == nil || r.controller == nil {
		return ContextEvidence{}, errors.New("workspace repository is not configured")
	}
	return r.controller.ContextEvidence(ctx, cwd, target)
}

func (r *Repository) Status(ctx context.Context, cwd string) (map[string]any, error) {
	return r.controller.Status(ctx, cwd)
}

func (r *Repository) Diff(ctx context.Context, cwd string, staged bool, path *string) (map[string]any, error) {
	return r.controller.Diff(ctx, cwd, staged, path)
}

func (r *Repository) Index(ctx context.Context, cwd string) (map[string]any, error) {
	return r.controller.Index(ctx, cwd)
}

func (r *Repository) MutatePaths(ctx context.Context, operation, cwd string, paths []string, expected *string) (map[string]any, error) {
	return r.controller.MutatePaths(ctx, operation, cwd, paths, expected)
}

func (r *Repository) StagePatch(ctx context.Context, cwd, patch string, reverse bool, expected *string) (map[string]any, error) {
	return r.controller.StagePatch(ctx, cwd, patch, reverse, expected)
}

func (r *Repository) CommitContext(ctx context.Context, cwd string) (map[string]any, error) {
	return r.controller.CommitContext(ctx, cwd)
}

func (r *Repository) Checkpoint(ctx context.Context, cwd string) (*string, error) {
	return r.controller.Checkpoint(ctx, cwd)
}

func (r *Repository) ReadCheckpoint(id string) (CheckpointMetadata, error) {
	return r.controller.ReadCheckpoint(id)
}

func (r *Repository) ListCheckpoints() ([]string, error) {
	return r.controller.ListCheckpoints()
}

func (r *Repository) RestoreCheckpoint(ctx context.Context, id string) (CheckpointMetadata, error) {
	return r.controller.RestoreCheckpoint(ctx, id)
}
