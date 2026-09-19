package devtools

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
)

const (
	maxSkillResources    = 512
	maxSkillContentBytes = 512 << 10
	maxSkillTotalBytes   = 16 << 20
	maxGuidanceSources   = 32
	maxGuidanceFileBytes = 512 << 10
	maxGuidanceTotal     = 2 << 20
)

type SkillSummary struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	Scope         string            `json:"scope"`
	Revision      string            `json:"revision"`
	License       string            `json:"license,omitempty"`
	Compatibility string            `json:"compatibility,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	AllowedTools  string            `json:"allowed_tools,omitempty"`
	ResourceCount int               `json:"resource_count"`
	TotalBytes    int64             `json:"total_bytes"`
}

type SkillDiagnostic struct {
	Scope   string `json:"scope"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type SkillShadow struct {
	Name          string `json:"name"`
	SelectedScope string `json:"selected_scope"`
	ShadowedScope string `json:"shadowed_scope"`
}

type SkillCatalog struct {
	Items       []SkillSummary    `json:"items"`
	Diagnostics []SkillDiagnostic `json:"diagnostics"`
	Shadowed    []SkillShadow     `json:"shadowed"`
}

type SkillResource struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	Executable bool   `json:"executable"`
}

type SkillDetail struct {
	SkillSummary
	Content   string          `json:"content"`
	Resources []SkillResource `json:"resources"`
}

type SkillInspection struct {
	Item        SkillDetail       `json:"item"`
	Diagnostics []SkillDiagnostic `json:"diagnostics"`
	Shadowed    []SkillShadow     `json:"shadowed"`
}

type GuidanceSource struct {
	Path     string `json:"path"`
	Scope    string `json:"scope"`
	Revision string `json:"revision"`
	Bytes    int    `json:"bytes"`
	Content  string `json:"content"`
}

type GuidanceDiagnostic struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type GuidanceResult struct {
	Target      string               `json:"target"`
	TargetDir   string               `json:"target_dir"`
	Revision    string               `json:"revision"`
	Complete    bool                 `json:"complete"`
	Sources     []GuidanceSource     `json:"sources"`
	Diagnostics []GuidanceDiagnostic `json:"diagnostics"`
	TotalBytes  int                  `json:"total_bytes"`
}

func isAgentGuidanceCommand(name string) bool {
	switch name {
	case "skill list", "skill inspect", "guidance resolve":
		return true
	default:
		return false
	}
}

type skillWire struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	Scope         string            `json:"scope"`
	Root          string            `json:"root"`
	Revision      string            `json:"revision"`
	License       string            `json:"license"`
	Compatibility string            `json:"compatibility"`
	Metadata      map[string]string `json:"metadata"`
	AllowedTools  string            `json:"allowed_tools"`
	ResourceCount int               `json:"resource_count"`
	TotalBytes    int64             `json:"total_bytes"`
	Content       string            `json:"content"`
	Resources     []SkillResource   `json:"resources"`
}

type skillDiagnosticWire struct {
	Scope   string `json:"scope"`
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type skillShadowWire struct {
	Name          string `json:"name"`
	SelectedScope string `json:"selected_scope"`
	SelectedRoot  string `json:"selected_root"`
	ShadowedScope string `json:"shadowed_scope"`
	ShadowedRoot  string `json:"shadowed_root"`
}

func safeRelativePath(value string, allowDot bool) (string, error) {
	if value == "" {
		return "", errors.New("empty relative path")
	}
	if filepath.IsAbs(value) {
		return "", errors.New("absolute path is not allowed")
	}
	clean := filepath.Clean(value)
	if clean == "." {
		if allowDot {
			return ".", nil
		}
		return "", errors.New("dot path is not allowed")
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("path traversal is not allowed")
	}
	return filepath.ToSlash(clean), nil
}

func projectReadCall(ctx context.Context, c *Client, command, directory string, args []string) (json.RawMessage, error) {
	if directory == "" {
		directory = "."
	}
	if err := c.requireWorkspaceProject(directory); err != nil {
		return nil, err
	}
	input := map[string]any{"dir": directory}
	if len(args) > 0 {
		input["args"] = args
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, errors.New("encode devtools project read request")
	}
	return c.call(ctx, command, raw, c.Env)
}

func publicSkill(wire skillWire) (SkillSummary, error) {
	if wire.Name == "" || wire.Description == "" || (wire.Scope != "project" && wire.Scope != "user") ||
		wire.Revision == "" || wire.ResourceCount < 0 || wire.TotalBytes < 0 || wire.TotalBytes > maxSkillTotalBytes {
		return SkillSummary{}, errors.New("devtools returned invalid Skill metadata")
	}
	return SkillSummary{
		Name: wire.Name, Description: wire.Description, Scope: wire.Scope, Revision: wire.Revision,
		License: wire.License, Compatibility: wire.Compatibility, Metadata: wire.Metadata,
		AllowedTools: wire.AllowedTools, ResourceCount: wire.ResourceCount, TotalBytes: wire.TotalBytes,
	}, nil
}

func publicSkillDiagnostics(items []skillDiagnosticWire) ([]SkillDiagnostic, error) {
	out := make([]SkillDiagnostic, 0, len(items))
	for _, item := range items {
		if item.Scope != "project" && item.Scope != "user" {
			return nil, errors.New("devtools returned invalid Skill diagnostic")
		}
		out = append(out, SkillDiagnostic{Scope: item.Scope, Code: item.Code, Message: item.Message})
	}
	return out, nil
}

func publicSkillShadows(items []skillShadowWire) ([]SkillShadow, error) {
	out := make([]SkillShadow, 0, len(items))
	for _, item := range items {
		if item.Name == "" || item.SelectedScope == "" || item.ShadowedScope == "" {
			return nil, errors.New("devtools returned invalid Skill shadow metadata")
		}
		out = append(out, SkillShadow{Name: item.Name, SelectedScope: item.SelectedScope, ShadowedScope: item.ShadowedScope})
	}
	return out, nil
}

func (c *Client) ListSkills(ctx context.Context, directory string) (SkillCatalog, error) {
	raw, err := projectReadCall(ctx, c, "skill list", directory, nil)
	if err != nil {
		return SkillCatalog{}, err
	}
	var wire struct {
		Items       []skillWire           `json:"items"`
		Diagnostics []skillDiagnosticWire `json:"diagnostics"`
		Shadowed    []skillShadowWire     `json:"shadowed"`
	}
	if err := decodeObject(raw, &wire); err != nil {
		return SkillCatalog{}, errors.New("devtools returned invalid Skill catalog")
	}
	result := SkillCatalog{Items: []SkillSummary{}, Diagnostics: []SkillDiagnostic{}, Shadowed: []SkillShadow{}}
	for _, item := range wire.Items {
		public, publicErr := publicSkill(item)
		if publicErr != nil {
			return SkillCatalog{}, publicErr
		}
		result.Items = append(result.Items, public)
	}
	result.Diagnostics, err = publicSkillDiagnostics(wire.Diagnostics)
	if err != nil {
		return SkillCatalog{}, err
	}
	result.Shadowed, err = publicSkillShadows(wire.Shadowed)
	if err != nil {
		return SkillCatalog{}, err
	}
	return result, nil
}

func (c *Client) InspectSkill(ctx context.Context, directory, name string) (SkillInspection, error) {
	if name == "" {
		return SkillInspection{}, errors.New("devtools Skill name is required")
	}
	raw, err := projectReadCall(ctx, c, "skill inspect", directory, []string{name})
	if err != nil {
		return SkillInspection{}, err
	}
	var wire struct {
		Item        skillWire             `json:"item"`
		Diagnostics []skillDiagnosticWire `json:"diagnostics"`
		Shadowed    []skillShadowWire     `json:"shadowed"`
	}
	if err := decodeObject(raw, &wire); err != nil {
		return SkillInspection{}, errors.New("devtools returned invalid Skill inspection")
	}
	summary, err := publicSkill(wire.Item)
	if err != nil {
		return SkillInspection{}, err
	}
	if summary.Name != name || len(wire.Item.Content) > maxSkillContentBytes || len(wire.Item.Resources) > maxSkillResources {
		return SkillInspection{}, errors.New("devtools returned invalid Skill details")
	}
	resources := make([]SkillResource, 0, len(wire.Item.Resources))
	for _, resource := range wire.Item.Resources {
		path, pathErr := safeRelativePath(resource.Path, false)
		if pathErr != nil || resource.Size < 0 || resource.Size > 2<<20 || resource.SHA256 == "" {
			return SkillInspection{}, errors.New("devtools returned invalid Skill resource")
		}
		resource.Path = path
		resources = append(resources, resource)
	}
	diagnostics, err := publicSkillDiagnostics(wire.Diagnostics)
	if err != nil {
		return SkillInspection{}, err
	}
	shadowed, err := publicSkillShadows(wire.Shadowed)
	if err != nil {
		return SkillInspection{}, err
	}
	return SkillInspection{
		Item:        SkillDetail{SkillSummary: summary, Content: wire.Item.Content, Resources: resources},
		Diagnostics: diagnostics, Shadowed: shadowed,
	}, nil
}

func (c *Client) ResolveGuidance(ctx context.Context, directory, target string) (GuidanceResult, error) {
	if target == "" || filepath.IsAbs(target) {
		return GuidanceResult{}, errors.New("devtools guidance target must be relative")
	}
	if _, err := safeRelativePath(target, true); err != nil {
		return GuidanceResult{}, errors.New("devtools guidance target is unsafe")
	}
	raw, err := projectReadCall(ctx, c, "guidance resolve", directory, []string{target})
	if err != nil {
		return GuidanceResult{}, err
	}
	var result GuidanceResult
	if err := decodeObject(raw, &result); err != nil {
		return GuidanceResult{}, errors.New("devtools returned invalid guidance")
	}
	if len(result.Sources) > maxGuidanceSources || result.TotalBytes < 0 || result.TotalBytes > maxGuidanceTotal || result.Revision == "" {
		return GuidanceResult{}, errors.New("devtools returned invalid guidance bounds")
	}
	if result.Target, err = safeRelativePath(result.Target, true); err != nil {
		return GuidanceResult{}, errors.New("devtools returned unsafe guidance target")
	}
	if result.TargetDir, err = safeRelativePath(result.TargetDir, true); err != nil {
		return GuidanceResult{}, errors.New("devtools returned unsafe guidance target directory")
	}
	total := 0
	for index := range result.Sources {
		source := &result.Sources[index]
		if source.Path, err = safeRelativePath(source.Path, false); err != nil {
			return GuidanceResult{}, errors.New("devtools returned unsafe guidance source path")
		}
		if source.Scope, err = safeRelativePath(source.Scope, true); err != nil {
			return GuidanceResult{}, errors.New("devtools returned unsafe guidance scope")
		}
		if source.Bytes < 0 || source.Bytes > maxGuidanceFileBytes || source.Bytes != len(source.Content) || source.Revision == "" {
			return GuidanceResult{}, errors.New("devtools returned invalid guidance source")
		}
		total += source.Bytes
	}
	if total != result.TotalBytes {
		return GuidanceResult{}, errors.New("devtools guidance byte count is inconsistent")
	}
	for index := range result.Diagnostics {
		if result.Diagnostics[index].Path == "" {
			continue
		}
		if result.Diagnostics[index].Path, err = safeRelativePath(result.Diagnostics[index].Path, false); err != nil {
			return GuidanceResult{}, errors.New("devtools returned unsafe guidance diagnostic path")
		}
	}
	return result, nil
}
