package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"loki/internal/toolchain"
	"loki/internal/work/jobs"
)

type mcpToolchainResolver struct {
	resolver toolchain.ProjectResolver
}

func newMCPToolchainResolver(root, store, catalogPath string) (*mcpToolchainResolver, error) {
	for _, path := range []string{root, store, catalogPath} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
			return nil, errors.New("MCP toolchain paths must be clean absolute non-root paths")
		}
	}
	raw, err := os.ReadFile(catalogPath)
	if err != nil {
		return nil, err
	}
	catalog, err := toolchain.LoadCatalog(raw)
	if err != nil {
		return nil, err
	}
	return &mcpToolchainResolver{resolver: toolchain.ProjectResolver{
		Root: root, Store: toolchain.GenerationStore{Root: store}, Catalog: catalog,
	}}, nil
}

func (r *mcpToolchainResolver) Resolve(ctx context.Context, cwd string) ([]jobs.ToolchainRef, error) {
	if r == nil {
		return nil, errors.New("MCP toolchain resolver is not configured")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	selections, err := r.resolver.Resolve(cwd)
	if err != nil {
		return nil, err
	}
	result := make([]jobs.ToolchainRef, len(selections))
	for index, selection := range selections {
		result[index] = jobs.ToolchainRef{
			Family: selection.Family, Version: selection.Version, GenerationID: selection.GenerationID,
		}
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return result, nil
	}
}
