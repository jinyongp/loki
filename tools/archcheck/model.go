package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

type Policy struct {
	Version         int                    `json:"version"`
	Module          string                 `json:"module"`
	Variants        []Variant              `json:"variants"`
	Packages        map[string]PackageRule `json:"packages"`
	AllowedEdges    []Edge                 `json:"allowed_edges"`
	EdgeExceptions  []EdgeException        `json:"edge_exceptions"`
	ThirdPartyRules []ImportRule           `json:"third_party_rules"`
	ExportRules     []ImportRule           `json:"export_rules"`
	Roles           []RoleRule             `json:"roles"`
}

type Variant struct {
	Name   string   `json:"name"`
	GOOS   string   `json:"goos"`
	GOARCH string   `json:"goarch"`
	Tags   []string `json:"tags"`
}

type PackageRule struct {
	Kind   string   `json:"kind"`
	Owner  string   `json:"owner"`
	Target string   `json:"target"`
	Tags   []string `json:"tags"`
}

type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type EdgeException struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Reason     string `json:"reason"`
	RemoveUnit string `json:"remove_unit"`
}

type ImportRule struct {
	ImportPrefix string   `json:"import_prefix"`
	AllowedTags  []string `json:"allowed_tags"`
	Description  string   `json:"description"`
}

type RoleRule struct {
	Name          string          `json:"name"`
	Root          string          `json:"root"`
	Optional      bool            `json:"optional"`
	ForbiddenTags []string        `json:"forbidden_tags"`
	Exceptions    []RoleException `json:"exceptions,omitempty"`
}

type RoleException struct {
	Package    string `json:"package"`
	Reason     string `json:"reason"`
	RemoveUnit string `json:"remove_unit"`
}

type PackageInfo struct {
	ImportPath   string
	Dir          string
	GoFiles      []string
	CgoFiles     []string
	Imports      []string
	TestImports  []string
	XTestImports []string
}

type Graph struct {
	Packages map[string]PackageInfo
}

type Violation struct {
	Rule    string
	Package string
	Detail  string
}

func (v Violation) String() string {
	if v.Package == "" {
		return v.Rule + ": " + v.Detail
	}
	return v.Rule + ": " + v.Package + ": " + v.Detail
}

func loadPolicy(path string) (Policy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Policy{}, err
	}
	var policy Policy
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&policy); err != nil {
		return Policy{}, fmt.Errorf("decode architecture policy: %w", err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Policy{}, fmt.Errorf("decode architecture policy: trailing content")
		}
		return Policy{}, fmt.Errorf("decode architecture policy: %w", err)
	}
	return policy, validatePolicy(policy)
}
