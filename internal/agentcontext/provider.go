package agentcontext

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
	"loki/internal/policy"
)

type RepositoryResolver interface {
	RepositoryRoot(context.Context, string) (string, error)
}

type Provider struct {
	Paths          *policy.Workspace
	Git            RepositoryResolver
	UserHome       string
	PackagedSkills string
}

type ContextResult struct {
	Guidance GuidanceResult `json:"guidance"`
	Skills   SkillCatalog   `json:"skills"`
}

type projectScope struct {
	target string
	paths  *policy.Workspace
}

func outside(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative)
}

func (p *Provider) targetDirectory(target string) (string, error) {
	current := target
	for {
		handle, err := p.Paths.Open(current, unix.O_PATH, 0)
		if err == nil {
			info, statErr := handle.Stat()
			handle.Close()
			if statErr != nil {
				return "", statErr
			}
			if info.IsDir() {
				return filepath.ToSlash(filepath.Clean(current)), nil
			}
			if info.Mode().IsRegular() {
				return filepath.ToSlash(filepath.Dir(current)), nil
			}
			return "", errors.New("agent context target must be a regular file or directory")
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.ToSlash(filepath.Dir(current))
		if parent == current {
			return "", errors.New("agent context target has no existing parent")
		}
		current = parent
	}
}

func (p *Provider) project(ctx context.Context, cwd, target string) (*projectScope, error) {
	if p == nil || p.Paths == nil || p.Git == nil {
		return nil, errors.New("agent context provider is unavailable")
	}
	if cwd == "" {
		cwd = "."
	}
	if target == "" {
		target = "."
	}
	if filepath.IsAbs(target) {
		return nil, errors.New("agent context target must be relative")
	}
	target, err := policy.Relative(filepath.ToSlash(target))
	if err != nil {
		return nil, err
	}
	absoluteCWD, err := p.Paths.ResolveCWD(cwd)
	if err != nil {
		return nil, err
	}
	relativeCWD, err := filepath.Rel(p.Paths.Root(), absoluteCWD)
	if err != nil || outside(p.Paths.Root(), absoluteCWD) {
		return nil, errors.New("agent context cwd is outside the workspace")
	}
	workspaceTarget, err := policy.Relative(filepath.ToSlash(filepath.Join(relativeCWD, target)))
	if err != nil {
		return nil, err
	}
	ownerDirectory, err := p.targetDirectory(workspaceTarget)
	if err != nil {
		return nil, err
	}
	root, err := p.Git.RepositoryRoot(ctx, ownerDirectory)
	if err != nil {
		return nil, err
	}
	absoluteTarget := filepath.Join(p.Paths.Root(), filepath.FromSlash(workspaceTarget))
	if outside(root, absoluteTarget) {
		return nil, errors.New("agent context target is outside its repository")
	}
	repositoryTarget, err := filepath.Rel(root, absoluteTarget)
	if err != nil {
		return nil, err
	}
	paths, err := policy.New(root)
	if err != nil {
		return nil, err
	}
	return &projectScope{target: filepath.ToSlash(repositoryTarget), paths: paths}, nil
}

func (p *Provider) Context(ctx context.Context, cwd, target string) (ContextResult, error) {
	project, err := p.project(ctx, cwd, target)
	if err != nil {
		return ContextResult{}, err
	}
	defer project.paths.Close()

	guidance, err := resolveGuidance(project.paths, ".", project.target)
	if err != nil {
		return ContextResult{}, err
	}
	skills, err := p.listSkills(project.paths)
	if err != nil {
		return ContextResult{}, err
	}
	return ContextResult{Guidance: guidance, Skills: skills}, nil
}

func (p *Provider) Skill(ctx context.Context, cwd, target, name string) (SkillInspection, error) {
	project, err := p.project(ctx, cwd, target)
	if err != nil {
		return SkillInspection{}, err
	}
	defer project.paths.Close()
	return p.inspectEffectiveSkill(project.paths, name)
}
