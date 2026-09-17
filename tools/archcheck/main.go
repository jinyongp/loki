// archcheck enforces Loki's checked-in package ownership and dependency policy.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type summary struct {
	Variants           []string        `json:"variants"`
	Packages           int             `json:"packages"`
	ProductionEdges    int             `json:"production_edges"`
	BaselineExceptions []EdgeException `json:"baseline_exceptions"`
}

func main() {
	rootFlag := flag.String("root", ".", "module root")
	policyFlag := flag.String("policy", "tools/archcheck/policy.json", "architecture policy path relative to root")
	flag.Parse()

	root, err := filepath.Abs(*rootFlag)
	if err != nil {
		fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		fatal(err)
	}
	policyPath := *policyFlag
	if !filepath.IsAbs(policyPath) {
		policyPath = filepath.Join(root, policyPath)
	}
	policy, err := loadPolicy(policyPath)
	if err != nil {
		fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	graphs := make([]Graph, 0, len(policy.Variants))
	var violations []Violation
	variantNames := make([]string, 0, len(policy.Variants))
	for _, variant := range policy.Variants {
		graph, loadErr := loadGraph(ctx, root, policy, variant)
		if loadErr != nil {
			fatal(loadErr)
		}
		graphs = append(graphs, graph)
		variantNames = append(variantNames, variant.Name)
		violations = append(violations, checkVariant(policy, graph)...)
	}
	violations = append(violations, checkBaselineExact(policy, graphs)...)
	violations = deduplicateViolations(violations)
	if len(violations) > 0 {
		for _, violation := range sortedViolations(violations) {
			fmt.Fprintln(os.Stderr, violation.String())
		}
		os.Exit(1)
	}

	edges := map[string]bool{}
	packages := map[string]bool{}
	for _, graph := range graphs {
		for name, item := range graph.Packages {
			packages[name] = true
			for _, dependency := range item.Imports {
				if _, ok := policy.Packages[dependency]; ok {
					edges[edgeKey(name, dependency)] = true
				}
			}
		}
	}
	exceptions := append([]EdgeException(nil), policy.EdgeExceptions...)
	sort.Slice(exceptions, func(i, j int) bool {
		return edgeKey(exceptions[i].From, exceptions[i].To) < edgeKey(exceptions[j].From, exceptions[j].To)
	})
	encoded, err := json.MarshalIndent(summary{
		Variants: variantNames, Packages: len(packages), ProductionEdges: len(edges), BaselineExceptions: exceptions,
	}, "", "  ")
	if err != nil {
		fatal(err)
	}
	fmt.Println(string(encoded))
}

func deduplicateViolations(items []Violation) []Violation {
	seen := map[string]bool{}
	out := make([]Violation, 0, len(items))
	for _, item := range items {
		key := item.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	return out
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "archcheck:", err)
	os.Exit(1)
}
