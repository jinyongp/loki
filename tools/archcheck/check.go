package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func validatePolicy(policy Policy) error {
	if policy.Version != 1 || policy.Module == "" {
		return fmt.Errorf("architecture policy requires version 1 and module")
	}
	if len(policy.Variants) == 0 || len(policy.Packages) == 0 {
		return fmt.Errorf("architecture policy requires variants and packages")
	}
	variantNames := map[string]bool{}
	for _, variant := range policy.Variants {
		if variant.Name == "" || variant.GOOS == "" || variant.GOARCH == "" || variantNames[variant.Name] {
			return fmt.Errorf("invalid or duplicate architecture variant %q", variant.Name)
		}
		variantNames[variant.Name] = true
	}
	for name, item := range policy.Packages {
		if !strings.HasPrefix(name, policy.Module+"/") || item.Kind == "" || item.Owner == "" || item.Target == "" {
			return fmt.Errorf("invalid package classification %q", name)
		}
	}
	seenEdges := map[string]string{}
	for _, edge := range policy.AllowedEdges {
		if err := validateEdge(policy, edge.From, edge.To); err != nil {
			return err
		}
		key := edgeKey(edge.From, edge.To)
		if seenEdges[key] != "" {
			return fmt.Errorf("duplicate architecture edge %s", key)
		}
		seenEdges[key] = "allowed"
	}
	for _, edge := range policy.EdgeExceptions {
		if err := validateEdge(policy, edge.From, edge.To); err != nil {
			return err
		}
		if edge.Reason == "" || edge.RemoveUnit == "" {
			return fmt.Errorf("architecture exception %s -> %s needs reason and remove_unit", edge.From, edge.To)
		}
		key := edgeKey(edge.From, edge.To)
		if seenEdges[key] != "" {
			return fmt.Errorf("duplicate architecture edge %s", key)
		}
		seenEdges[key] = "exception"
	}
	for _, rule := range append(append([]ImportRule{}, policy.ThirdPartyRules...), policy.ExportRules...) {
		if rule.ImportPrefix == "" || len(rule.AllowedTags) == 0 || rule.Description == "" {
			return fmt.Errorf("invalid third-party/export rule")
		}
	}
	for _, role := range policy.Roles {
		if role.Name == "" || role.Root == "" || len(role.ForbiddenTags) == 0 {
			return fmt.Errorf("invalid role rule %q", role.Name)
		}
		for _, exception := range role.Exceptions {
			if exception.Package == "" || exception.Reason == "" || exception.RemoveUnit == "" {
				return fmt.Errorf("role exception in %q needs package, reason and remove_unit", role.Name)
			}
		}
	}
	return nil
}

func validateEdge(policy Policy, from, to string) error {
	if policy.Packages[from].Kind == "" || policy.Packages[to].Kind == "" {
		return fmt.Errorf("architecture edge references unclassified package: %s -> %s", from, to)
	}
	return nil
}

func edgeKey(from, to string) string { return from + " -> " + to }

func packageHasAnyTag(rule PackageRule, tags []string) bool {
	for _, have := range rule.Tags {
		for _, want := range tags {
			if have == want {
				return true
			}
		}
	}
	return false
}

func importMatches(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func checkVariant(policy Policy, graph Graph) []Violation {
	var violations []Violation
	for name := range graph.Packages {
		if _, ok := policy.Packages[name]; !ok {
			violations = append(violations, Violation{"package-classification", name, "package is not classified"})
		}
	}
	for name := range policy.Packages {
		if _, ok := graph.Packages[name]; !ok {
			violations = append(violations, Violation{"package-classification", name, "classified package is absent from this supported variant"})
		}
	}

	allowed := map[string]bool{}
	for _, edge := range policy.AllowedEdges {
		allowed[edgeKey(edge.From, edge.To)] = true
	}
	exceptions := map[string]bool{}
	for _, edge := range policy.EdgeExceptions {
		exceptions[edgeKey(edge.From, edge.To)] = true
	}

	for name, item := range graph.Packages {
		for _, dependency := range item.Imports {
			if strings.HasPrefix(dependency, policy.Module+"/") {
				if _, ok := policy.Packages[dependency]; !ok {
					violations = append(violations, Violation{"package-classification", name, "imports unclassified package " + dependency})
				} else if key := edgeKey(name, dependency); !allowed[key] && !exceptions[key] {
					violations = append(violations, Violation{"direct-dependency", name, "new in-module edge is not approved: " + dependency})
				}
				if !internalImportAllowed(name, dependency) {
					violations = append(violations, Violation{"private-package", name, "imports private package " + dependency})
				}
			}
			violations = append(violations, checkThirdParty(policy, name, dependency, "production")...)
		}
		for _, dependency := range append(append([]string{}, item.TestImports...), item.XTestImports...) {
			if strings.HasPrefix(dependency, policy.Module+"/") && !internalImportAllowed(name, dependency) {
				violations = append(violations, Violation{"private-package-test", name, "test imports private package " + dependency})
			}
			violations = append(violations, checkThirdParty(policy, name, dependency, "test")...)
		}
		violations = append(violations, checkExportRules(policy, item)...)
	}
	violations = append(violations, checkRoles(policy, graph)...)
	return violations
}

func checkBaselineExact(policy Policy, graphs []Graph) []Violation {
	actual := map[string]bool{}
	present := map[string]bool{}
	for _, graph := range graphs {
		for name, item := range graph.Packages {
			present[name] = true
			for _, dependency := range item.Imports {
				if strings.HasPrefix(dependency, policy.Module+"/") {
					actual[edgeKey(name, dependency)] = true
				}
			}
		}
	}
	var violations []Violation
	for name := range policy.Packages {
		if !present[name] {
			violations = append(violations, Violation{"package-baseline", name, "classified package is absent from all supported variants"})
		}
	}
	for _, edge := range policy.AllowedEdges {
		if !actual[edgeKey(edge.From, edge.To)] {
			violations = append(violations, Violation{"edge-baseline", edge.From, "approved edge is stale: " + edge.To})
		}
	}
	for _, edge := range policy.EdgeExceptions {
		if !actual[edgeKey(edge.From, edge.To)] {
			violations = append(violations, Violation{"exception-baseline", edge.From, "migration exception is stale and must be removed: " + edge.To + " (" + edge.RemoveUnit + ")"})
		}
	}
	return violations
}

func checkThirdParty(policy Policy, importer, dependency, scope string) []Violation {
	rule, ok := policy.Packages[importer]
	if !ok {
		return nil
	}
	var violations []Violation
	for _, restriction := range policy.ThirdPartyRules {
		if importMatches(dependency, restriction.ImportPrefix) && !packageHasAnyTag(rule, restriction.AllowedTags) {
			violations = append(violations, Violation{"third-party-owner", importer, scope + " import " + dependency + " violates: " + restriction.Description})
		}
	}
	return violations
}

func internalImportAllowed(importer, dependency string) bool {
	parts := strings.Split(dependency, "/")
	lastInternal := -1
	for index, part := range parts {
		if part == "internal" {
			lastInternal = index
		}
	}
	if lastInternal < 0 {
		return true
	}
	parent := strings.Join(parts[:lastInternal], "/")
	return importer == parent || strings.HasPrefix(importer, parent+"/")
}

func checkRoles(policy Policy, graph Graph) []Violation {
	var violations []Violation
	for _, role := range policy.Roles {
		if _, ok := graph.Packages[role.Root]; !ok {
			if !role.Optional {
				violations = append(violations, Violation{"role-root", role.Root, "required role root is absent"})
			}
			continue
		}
		exceptions := map[string]RoleException{}
		for _, exception := range role.Exceptions {
			exceptions[exception.Package] = exception
		}
		seen := map[string]bool{}
		queue := []string{role.Root}
		for len(queue) > 0 {
			name := queue[0]
			queue = queue[1:]
			if seen[name] {
				continue
			}
			seen[name] = true
			if rule, ok := policy.Packages[name]; ok && packageHasAnyTag(rule, role.ForbiddenTags) {
				if _, excepted := exceptions[name]; !excepted {
					violations = append(violations, Violation{"role-closure", role.Root, role.Name + " reaches forbidden package " + name})
				}
			}
			for _, dependency := range graph.Packages[name].Imports {
				if _, ok := graph.Packages[dependency]; ok && !seen[dependency] {
					queue = append(queue, dependency)
				}
			}
		}
	}
	return violations
}

func checkExportRules(policy Policy, item PackageInfo) []Violation {
	packageRule, ok := policy.Packages[item.ImportPath]
	if !ok || item.Dir == "" {
		return nil
	}
	var active []ImportRule
	for _, rule := range policy.ExportRules {
		if !packageHasAnyTag(packageRule, rule.AllowedTags) {
			active = append(active, rule)
		}
	}
	if len(active) == 0 {
		return nil
	}
	var violations []Violation
	for _, file := range append(append([]string{}, item.GoFiles...), item.CgoFiles...) {
		path := filepath.Join(item.Dir, file)
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			violations = append(violations, Violation{"export-scan", item.ImportPath, path + ": " + err.Error()})
			continue
		}
		violations = append(violations, exportedImportViolations(policy, item.ImportPath, parsed, active)...)
	}
	return violations
}

func exportedImportViolations(policy Policy, packageName string, file *ast.File, rules []ImportRule) []Violation {
	aliases := map[string]ImportRule{}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		for _, rule := range rules {
			if !importMatches(path, rule.ImportPrefix) {
				continue
			}
			name := filepath.Base(path)
			if spec.Name != nil {
				name = spec.Name.Name
			}
			if name == "." {
				return []Violation{{"export-type", packageName, "dot-import of restricted API " + path}}
			}
			if name != "_" {
				aliases[name] = rule
			}
		}
	}
	if len(aliases) == 0 {
		return nil
	}
	var violations []Violation
	inspectType := func(node ast.Node, declaration string) {
		ast.Inspect(node, func(child ast.Node) bool {
			selector, ok := child.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			identifier, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			if rule, restricted := aliases[identifier.Name]; restricted {
				violations = append(violations, Violation{"export-type", packageName, declaration + " leaks restricted API: " + rule.Description})
				return false
			}
			return true
		})
	}
	for _, declaration := range file.Decls {
		switch value := declaration.(type) {
		case *ast.FuncDecl:
			if value.Name.IsExported() {
				inspectType(value.Type, "func "+value.Name.Name)
			}
		case *ast.GenDecl:
			for _, spec := range value.Specs {
				switch item := spec.(type) {
				case *ast.TypeSpec:
					if item.Name.IsExported() {
						inspectType(item.Type, "type "+item.Name.Name)
					}
				case *ast.ValueSpec:
					exported := false
					for _, name := range item.Names {
						exported = exported || name.IsExported()
					}
					if exported && item.Type != nil {
						inspectType(item.Type, "exported value")
					}
				}
			}
		}
	}
	return violations
}

func sortedViolations(items []Violation) []Violation {
	sort.Slice(items, func(i, j int) bool { return items[i].String() < items[j].String() })
	return items
}
