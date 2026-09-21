package workspace

import (
	"context"
	"errors"

	"loki/internal/config"
	"loki/internal/policy"
	"loki/internal/work/jobs"
	gitops "loki/internal/work/workspace/git"
)

type ContextEvidence = gitops.ContextEvidence
type CheckpointMetadata = gitops.CheckpointMetadata

type Repository struct {
	controller *gitops.Controller
	runner     gitops.Runner
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
	controller := &gitops.Controller{
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
	commandRunner := gitops.JobRunner{Jobs: runner}
	r.runner = commandRunner
	r.controller.Runner = commandRunner
	return nil
}

func (f *Files) AttachRepository(repository *Repository) error {
	if f == nil || f.Policy == nil || repository == nil || repository.controller == nil || repository.runner == nil {
		return errors.New("workspace repository is not configured")
	}
	if repository.controller.Paths != f.Policy {
		return errors.New("workspace repository paths do not match files")
	}
	f.gitRunner = repository.runner
	f.repository = repository
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
