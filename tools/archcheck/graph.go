package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
)

type goListPackage struct {
	ImportPath   string
	Dir          string
	GoFiles      []string
	CgoFiles     []string
	Imports      []string
	TestImports  []string
	XTestImports []string
}

func loadGraph(ctx context.Context, root string, policy Policy, variant Variant) (Graph, error) {
	arguments := []string{
		"list", "-mod=readonly",
		"-json=ImportPath,Dir,GoFiles,CgoFiles,Imports,TestGoFiles,TestImports,XTestGoFiles,XTestImports",
	}
	if variant.Partial {
		arguments = append(arguments, "-deps")
	}
	if len(variant.Tags) > 0 {
		arguments = append(arguments, "-tags", strings.Join(variant.Tags, ","))
	}
	patterns := append([]string(nil), variant.Patterns...)
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	arguments = append(arguments, patterns...)
	command := exec.CommandContext(ctx, "go", arguments...)
	command.Dir = root
	command.Env = variantEnvironment(os.Environ(), variant)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return Graph{}, fmt.Errorf("go list %s: %w: %s", variant.Name, err, strings.TrimSpace(stderr.String()))
	}
	decoder := json.NewDecoder(&stdout)
	graph := Graph{Packages: map[string]PackageInfo{}}
	for {
		var item goListPackage
		if err := decoder.Decode(&item); err != nil {
			if err == io.EOF {
				break
			}
			return Graph{}, fmt.Errorf("decode go list %s: %w", variant.Name, err)
		}
		if item.ImportPath != policy.Module && !strings.HasPrefix(item.ImportPath, policy.Module+"/") {
			continue
		}
		graph.Packages[item.ImportPath] = PackageInfo{
			ImportPath: item.ImportPath, Dir: item.Dir,
			GoFiles: sortedCopy(item.GoFiles), CgoFiles: sortedCopy(item.CgoFiles),
			Imports: sortedCopy(item.Imports), TestImports: sortedCopy(item.TestImports), XTestImports: sortedCopy(item.XTestImports),
		}
	}
	if len(graph.Packages) == 0 {
		return Graph{}, fmt.Errorf("go list %s returned no module packages", variant.Name)
	}
	return graph, nil
}

func sortedCopy(items []string) []string {
	out := append([]string(nil), items...)
	sort.Strings(out)
	return out
}

func variantEnvironment(environment []string, variant Variant) []string {
	filtered := make([]string, 0, len(environment)+2)
	for _, entry := range environment {
		if strings.HasPrefix(entry, "GOOS=") || strings.HasPrefix(entry, "GOARCH=") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, "GOOS="+variant.GOOS, "GOARCH="+variant.GOARCH)
}
