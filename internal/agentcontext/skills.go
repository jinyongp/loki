package agentcontext

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
	"golang.org/x/sys/unix"
	"loki/internal/policy"
)

const (
	maxSkills            = 256
	maxSkillFiles        = 512
	maxSkillNodes        = 1024
	maxSkillDepth        = 32
	maxSkillBytes        = 512 << 10
	maxResourceBytes     = 2 << 20
	maxSkillTotal        = 16 << 20
	maxSkillCatalogFiles = 4096
	maxSkillCatalogBytes = 64 << 20
)

var skillNamePattern = regexp.MustCompile("^[a-z0-9]+(?:-[a-z0-9]+)*$")

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
	Complete    bool              `json:"complete"`
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

type skillValidationError struct {
	code    string
	message string
}

func (e *skillValidationError) Error() string { return e.message }

type skillFrontmatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license"`
	Compatibility string            `yaml:"compatibility"`
	Metadata      map[string]string `yaml:"metadata"`
	AllowedTools  string            `yaml:"allowed-tools"`
}

type skillFile struct {
	path       string
	data       []byte
	executable bool
}

type skillSource struct {
	scope string
	base  string
	paths *policy.Workspace
}

type skillCandidate struct {
	summary SkillSummary
	source  skillSource
}

type skillCatalogBudget struct {
	files     int
	bytes     int64
	maxFiles  int
	maxBytes  int64
	exhausted bool
}

func newSkillCatalogBudget() *skillCatalogBudget {
	return &skillCatalogBudget{maxFiles: maxSkillCatalogFiles, maxBytes: maxSkillCatalogBytes}
}

func (b *skillCatalogBudget) readLimit(limit int64) (int64, error) {
	if b == nil {
		return limit + 1, nil
	}
	if b.files >= b.maxFiles || b.bytes >= b.maxBytes {
		b.exhausted = true
		return 0, skillError("catalog_budget_exceeded", "Skill catalog discovery exceeded its aggregate read budget.")
	}
	remaining := b.maxBytes - b.bytes
	return min(limit+1, remaining+1), nil
}

func (b *skillCatalogBudget) account(size int) error {
	if b == nil {
		return nil
	}
	if b.files+1 > b.maxFiles || b.bytes+int64(size) > b.maxBytes {
		b.exhausted = true
		return skillError("catalog_budget_exceeded", "Skill catalog discovery exceeded its aggregate read budget.")
	}
	b.files++
	b.bytes += int64(size)
	return nil
}

func skillError(code, message string) error {
	return &skillValidationError{code: code, message: message}
}

func parseSkillFrontmatter(data []byte) (skillFrontmatter, error) {
	if len(data) > maxSkillBytes {
		return skillFrontmatter{}, skillError("skill_too_large", "SKILL.md exceeds the size limit.")
	}
	if !utf8.Valid(data) {
		return skillFrontmatter{}, skillError("invalid_utf8", "SKILL.md must be valid UTF-8.")
	}
	lines := bytes.Split(data, []byte("\n"))
	if len(lines) < 3 || string(bytes.TrimSuffix(lines[0], []byte("\r"))) != "---" {
		return skillFrontmatter{}, skillError("missing_frontmatter", "SKILL.md must start with YAML frontmatter.")
	}
	end := -1
	for index := 1; index < len(lines); index++ {
		if string(bytes.TrimSuffix(lines[index], []byte("\r"))) == "---" {
			end = index
			break
		}
	}
	if end < 0 {
		return skillFrontmatter{}, skillError("missing_frontmatter", "SKILL.md frontmatter is not closed.")
	}
	raw := bytes.Join(lines[1:end], []byte("\n"))
	var node yaml.Node
	if err := yaml.Unmarshal(raw, &node); err != nil || len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		return skillFrontmatter{}, skillError("invalid_yaml", "SKILL.md frontmatter is invalid YAML.")
	}
	mapping := node.Content[0]
	seen := map[string]bool{}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		key := mapping.Content[index].Value
		if seen[key] {
			return skillFrontmatter{}, skillError("duplicate_field", "SKILL.md frontmatter contains a duplicate field.")
		}
		seen[key] = true
	}
	var frontmatter skillFrontmatter
	if err := mapping.Decode(&frontmatter); err != nil {
		return skillFrontmatter{}, skillError("invalid_yaml", "SKILL.md frontmatter cannot be decoded.")
	}
	if len(frontmatter.Name) == 0 || len(frontmatter.Name) > 64 || !skillNamePattern.MatchString(frontmatter.Name) {
		return skillFrontmatter{}, skillError("invalid_name", "Skill name must use lowercase letters, numbers, and single hyphens.")
	}
	if strings.TrimSpace(frontmatter.Description) == "" || utf8.RuneCountInString(frontmatter.Description) > 1024 {
		return skillFrontmatter{}, skillError("invalid_description", "Skill description must be non-empty and at most 1024 characters.")
	}
	if utf8.RuneCountInString(frontmatter.Compatibility) > 500 {
		return skillFrontmatter{}, skillError("invalid_compatibility", "Skill compatibility must be at most 500 characters.")
	}
	return frontmatter, nil
}

func readSkillFile(paths *policy.Workspace, path string, limit int64, budget *skillCatalogBudget) (skillFile, error) {
	handle, err := paths.Open(path, os.O_RDONLY, 0)
	if err != nil {
		return skillFile{}, err
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil {
		return skillFile{}, err
	}
	if !info.Mode().IsRegular() {
		return skillFile{}, skillError("unsupported_file", "Skill trees can contain only regular files and directories.")
	}
	if info.Size() > limit {
		return skillFile{}, skillError("file_too_large", "Skill file exceeds its size limit.")
	}
	readLimit, err := budget.readLimit(limit)
	if err != nil {
		return skillFile{}, err
	}
	data, err := io.ReadAll(io.LimitReader(handle, readLimit))
	if err != nil {
		return skillFile{}, err
	}
	if int64(len(data)) > limit {
		return skillFile{}, skillError("file_too_large", "Skill file exceeds its size limit.")
	}
	if err := budget.account(len(data)); err != nil {
		return skillFile{}, err
	}
	return skillFile{path: filepath.ToSlash(path), data: data, executable: info.Mode().Perm()&0111 != 0}, nil
}

func scanSkill(source skillSource, name string, includeContent bool, budget *skillCatalogBudget) (SkillDetail, error) {
	if !skillNamePattern.MatchString(name) {
		return SkillDetail{}, skillError("invalid_name", "Skill directory name is invalid.")
	}
	root := filepath.ToSlash(filepath.Join(source.base, name))
	dir, err := source.paths.Open(root, os.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return SkillDetail{}, err
	}
	dir.Close()

	files := []skillFile{}
	nodes := 0
	total := int64(0)
	var walk func(string, int) error
	walk = func(current string, depth int) error {
		if depth > maxSkillDepth {
			return skillError("skill_too_deep", "Skill tree exceeds the directory depth limit.")
		}
		handle, err := source.paths.Open(current, os.O_RDONLY|unix.O_DIRECTORY, 0)
		if err != nil {
			return err
		}
		remaining := maxSkillNodes - nodes
		entries, readErr := handle.ReadDir(remaining + 1)
		handle.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if len(entries) > remaining {
			return skillError("too_many_entries", "Skill tree contains too many entries.")
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			nodes++
			child := filepath.ToSlash(filepath.Join(current, entry.Name()))
			if entry.Type()&os.ModeSymlink != 0 {
				return skillError("unsupported_symlink", "Skill trees cannot contain symbolic links.")
			}
			probe, err := source.paths.Open(child, unix.O_PATH, 0)
			if err != nil {
				return err
			}
			info, err := probe.Stat()
			probe.Close()
			if err != nil {
				return err
			}
			if info.IsDir() {
				if err := walk(child, depth+1); err != nil {
					return err
				}
				continue
			}
			if !info.Mode().IsRegular() {
				return skillError("unsupported_file", "Skill trees can contain only regular files and directories.")
			}
			if len(files) >= maxSkillFiles {
				return skillError("too_many_files", "Skill tree contains too many files.")
			}
			limit := int64(maxResourceBytes)
			relative, err := filepath.Rel(filepath.FromSlash(root), filepath.FromSlash(child))
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if relative == "SKILL.md" {
				limit = maxSkillBytes
			}
			file, err := readSkillFile(source.paths, child, limit, budget)
			if err != nil {
				return err
			}
			file.path = relative
			total += int64(len(file.data))
			if total > maxSkillTotal {
				return skillError("skill_too_large", "Skill tree exceeds the total size limit.")
			}
			files = append(files, file)
		}
		return nil
	}
	if err := walk(root, 0); err != nil {
		return SkillDetail{}, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })

	var skillData []byte
	for _, file := range files {
		if file.path == "SKILL.md" {
			skillData = file.data
			break
		}
	}
	if skillData == nil {
		return SkillDetail{}, skillError("missing_skill_file", "Skill directory does not contain SKILL.md.")
	}
	frontmatter, err := parseSkillFrontmatter(skillData)
	if err != nil {
		return SkillDetail{}, err
	}
	if frontmatter.Name != name {
		return SkillDetail{}, skillError("name_mismatch", "Skill name must match its parent directory.")
	}

	hasher := sha256.New()
	resources := []SkillResource{}
	for _, file := range files {
		sum := sha256.Sum256(file.data)
		digest := hex.EncodeToString(sum[:])
		_, _ = hasher.Write([]byte(file.path))
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write([]byte(digest))
		_, _ = hasher.Write([]byte{0})
		if file.executable {
			_, _ = hasher.Write([]byte("x"))
		} else {
			_, _ = hasher.Write([]byte("-"))
		}
		_, _ = hasher.Write([]byte{0})
		if file.path != "SKILL.md" {
			resources = append(resources, SkillResource{
				Path: file.path, Size: int64(len(file.data)), SHA256: digest, Executable: file.executable,
			})
		}
	}
	summary := SkillSummary{
		Name: frontmatter.Name, Description: frontmatter.Description, Scope: source.scope,
		Revision: hex.EncodeToString(hasher.Sum(nil)), License: frontmatter.License,
		Compatibility: frontmatter.Compatibility, Metadata: frontmatter.Metadata,
		AllowedTools: frontmatter.AllowedTools, ResourceCount: len(resources), TotalBytes: total,
	}
	detail := SkillDetail{SkillSummary: summary}
	if includeContent {
		detail.Content = string(skillData)
		detail.Resources = resources
	}
	return detail, nil
}

func validationDiagnostic(scope string, err error) SkillDiagnostic {
	var validation *skillValidationError
	if errors.As(err, &validation) {
		return SkillDiagnostic{Scope: scope, Code: validation.code, Message: validation.message}
	}
	return SkillDiagnostic{Scope: scope, Code: "unreadable", Message: "Skill cannot be read safely."}
}

func discoverSkillScope(source skillSource, budget *skillCatalogBudget) ([]skillCandidate, []SkillDiagnostic, bool) {
	if source.paths == nil {
		return []skillCandidate{}, []SkillDiagnostic{}, true
	}
	handle, err := source.paths.Open(source.base, os.O_RDONLY|unix.O_DIRECTORY, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return []skillCandidate{}, []SkillDiagnostic{}, true
	}
	if err != nil {
		return []skillCandidate{}, []SkillDiagnostic{{Scope: source.scope, Code: "invalid_registry", Message: "Skill registry cannot be read safely."}}, false
	}
	entries, readErr := handle.ReadDir(maxSkills + 1)
	handle.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return []skillCandidate{}, []SkillDiagnostic{{Scope: source.scope, Code: "unreadable_registry", Message: "Skill registry cannot be listed."}}, false
	}
	diagnostics := []SkillDiagnostic{}
	complete := true
	if len(entries) > maxSkills {
		entries = entries[:maxSkills]
		diagnostics = append(diagnostics, SkillDiagnostic{Scope: source.scope, Code: "too_many_skills", Message: "Skill discovery limit reached."})
		complete = false
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	items := []skillCandidate{}
	for _, entry := range entries {
		if budget != nil && budget.exhausted {
			complete = false
			break
		}
		if entry.Type()&os.ModeSymlink != 0 {
			diagnostics = append(diagnostics, SkillDiagnostic{Scope: source.scope, Code: "unsupported_symlink", Message: "Skill root cannot be a symbolic link."})
			complete = false
			continue
		}
		if !entry.IsDir() {
			continue
		}
		detail, err := scanSkill(source, entry.Name(), false, budget)
		if err != nil {
			diagnostics = append(diagnostics, validationDiagnostic(source.scope, err))
			complete = false
			if budget != nil && budget.exhausted {
				break
			}
			continue
		}
		items = append(items, skillCandidate{summary: detail.SkillSummary, source: source})
	}
	return items, diagnostics, complete
}

func (p *Provider) userSource() (skillSource, []SkillDiagnostic) {
	if p.UserHome == "" {
		return skillSource{}, []SkillDiagnostic{}
	}
	paths, err := policy.New(p.UserHome)
	if err != nil {
		return skillSource{}, []SkillDiagnostic{{Scope: "user", Code: "user_registry_unavailable", Message: "User Skill registry is unavailable."}}
	}
	return skillSource{scope: "user", base: filepath.ToSlash(filepath.Join(".agents", "skills")), paths: paths}, []SkillDiagnostic{}
}

func (p *Provider) packagedSource() (skillSource, []SkillDiagnostic) {
	if p.PackagedSkills == "" {
		return skillSource{}, []SkillDiagnostic{}
	}
	paths, err := policy.New(p.PackagedSkills)
	if err != nil {
		return skillSource{}, []SkillDiagnostic{{Scope: "packaged", Code: "packaged_registry_unavailable", Message: "Packaged Skill registry is unavailable."}}
	}
	return skillSource{scope: "packaged", base: ".", paths: paths}, []SkillDiagnostic{}
}

func (p *Provider) discoverSkills(projectPaths *policy.Workspace) (SkillCatalog, map[string]skillCandidate, func()) {
	return p.discoverSkillsWithBudget(projectPaths, newSkillCatalogBudget())
}

func (p *Provider) discoverSkillsWithBudget(projectPaths *policy.Workspace, budget *skillCatalogBudget) (SkillCatalog, map[string]skillCandidate, func()) {
	userSource, userDiagnostics := p.userSource()
	packagedSource, packagedDiagnostics := p.packagedSource()
	cleanup := func() {
		if userSource.paths != nil {
			_ = userSource.paths.Close()
		}
		if packagedSource.paths != nil {
			_ = packagedSource.paths.Close()
		}
	}

	catalog := SkillCatalog{
		Complete: true, Items: []SkillSummary{}, Diagnostics: []SkillDiagnostic{}, Shadowed: []SkillShadow{},
	}
	catalog.Diagnostics = append(catalog.Diagnostics, userDiagnostics...)
	catalog.Diagnostics = append(catalog.Diagnostics, packagedDiagnostics...)
	if len(userDiagnostics) > 0 || len(packagedDiagnostics) > 0 {
		catalog.Complete = false
	}

	selected := map[string]skillCandidate{}
	sources := []skillSource{
		{scope: "project", base: filepath.ToSlash(filepath.Join(".agents", "skills")), paths: projectPaths},
		userSource,
		packagedSource,
	}
	for _, source := range sources {
		if source.paths == nil {
			continue
		}
		items, diagnostics, complete := discoverSkillScope(source, budget)
		catalog.Diagnostics = append(catalog.Diagnostics, diagnostics...)
		if !complete {
			catalog.Complete = false
		}
		for _, item := range items {
			if prior, exists := selected[item.summary.Name]; exists {
				catalog.Shadowed = append(catalog.Shadowed, SkillShadow{
					Name: item.summary.Name, SelectedScope: prior.source.scope, ShadowedScope: item.source.scope,
				})
				continue
			}
			selected[item.summary.Name] = item
		}
		if budget.exhausted {
			break
		}
	}

	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		catalog.Items = append(catalog.Items, selected[name].summary)
	}
	sort.Slice(catalog.Diagnostics, func(i, j int) bool {
		if catalog.Diagnostics[i].Scope == catalog.Diagnostics[j].Scope {
			if catalog.Diagnostics[i].Code == catalog.Diagnostics[j].Code {
				return catalog.Diagnostics[i].Message < catalog.Diagnostics[j].Message
			}
			return catalog.Diagnostics[i].Code < catalog.Diagnostics[j].Code
		}
		return catalog.Diagnostics[i].Scope < catalog.Diagnostics[j].Scope
	})
	sort.Slice(catalog.Shadowed, func(i, j int) bool {
		if catalog.Shadowed[i].Name == catalog.Shadowed[j].Name {
			return catalog.Shadowed[i].ShadowedScope < catalog.Shadowed[j].ShadowedScope
		}
		return catalog.Shadowed[i].Name < catalog.Shadowed[j].Name
	})
	return catalog, selected, cleanup
}

func (p *Provider) listSkills(projectPaths *policy.Workspace) (SkillCatalog, error) {
	catalog, _, cleanup := p.discoverSkills(projectPaths)
	defer cleanup()
	return catalog, nil
}

func (p *Provider) inspectEffectiveSkill(projectPaths *policy.Workspace, name string) (SkillInspection, error) {
	if !skillNamePattern.MatchString(name) {
		return SkillInspection{}, errors.New("invalid Skill name")
	}
	catalog, selected, cleanup := p.discoverSkills(projectPaths)
	defer cleanup()
	candidate, ok := selected[name]
	if !ok {
		return SkillInspection{}, errors.New("selected Skill is not available")
	}
	detail, err := scanSkill(candidate.source, name, true, nil)
	if err != nil {
		return SkillInspection{}, err
	}
	return SkillInspection{Item: detail, Diagnostics: catalog.Diagnostics, Shadowed: catalog.Shadowed}, nil
}
