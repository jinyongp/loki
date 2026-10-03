package management

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"loki/internal/tools"
)

var imageDigestPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]*@sha256:[a-f0-9]{64}$`)
var payloadNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// FullPayload binds service images and native resources to the same trusted
// archive as the module manifest. The management binary acquires no implicit
// service image or ambient executable. Paths are scoped to one generation.
type FullPayload struct {
	Schema   int               `json:"schema"`
	Module   tools.ID          `json:"module"`
	Release  string            `json:"release"`
	Target   tools.Target      `json:"target"`
	Images   map[string]string `json:"images"`
	Programs map[string]string `json:"programs"`
	Assets   map[string]string `json:"assets"`
}

func (p FullPayload) Validate(artifact tools.Artifact) error {
	if p.Schema != 1 || p.Module != artifact.Module || p.Release != artifact.Release || p.Target != artifact.Target || p.Target.Mode != tools.Full || p.Target.OS != "linux" || p.Images == nil || p.Programs == nil || p.Assets == nil {
		return fmt.Errorf("full payload differs from its trusted module artifact")
	}
	allowedImages := map[tools.ID][]string{
		"runtime-core": {"service", "gateway"},
		"execution":    {"workload"},
		"browser":      {"browser"},
		"git":          {"git-workload"},
	}
	for name, image := range p.Images {
		allowed := false
		for _, role := range allowedImages[p.Module] {
			allowed = allowed || role == name
		}
		if !allowed || !imageDigestPattern.MatchString(image) {
			return fmt.Errorf("invalid or unowned %s image in module %s", name, p.Module)
		}
	}
	for _, resources := range []map[string]string{p.Programs, p.Assets} {
		for name, relative := range resources {
			if !payloadNamePattern.MatchString(name) || !filepath.IsLocal(relative) || relative == "." {
				return fmt.Errorf("invalid module-owned payload path")
			}
		}
	}
	return nil
}

func loadFullPayload(generation string, artifact tools.Artifact) (FullPayload, error) {
	var payload FullPayload
	if err := verifyOwner(generation, artifact); err != nil {
		return payload, err
	}
	if err := readOwnedJSON(filepath.Join(generation, "full-runtime.json"), tools.MaxManifestBytes, &payload); err != nil {
		return payload, err
	}
	if err := payload.Validate(artifact); err != nil {
		return payload, err
	}
	root, err := os.OpenRoot(generation)
	if err != nil {
		return payload, err
	}
	defer root.Close()
	for _, resources := range []map[string]string{payload.Programs, payload.Assets} {
		for _, relative := range resources {
			info, err := root.Lstat(relative)
			if err != nil {
				return payload, err
			}
			if info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) {
				return payload, fmt.Errorf("full payload resource must be a real owned file or directory")
			}
		}
	}
	for _, relative := range payload.Programs {
		info, err := root.Stat(relative)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return payload, fmt.Errorf("full payload program must be an executable regular file")
		}
	}
	return payload, nil
}

type FullResources struct {
	Plan     FullPlan                 `json:"plan"`
	Payloads map[tools.ID]FullPayload `json:"payloads"`
}

// FullResources checks every active private prerequisite. Installed-but-disabled
// modules are absent, so a browser-only deployment cannot acquire Git/provider
// programs or retain credentials through this resource plan.
func (s Store) FullResources() (FullResources, error) {
	plan, err := s.PlanFull()
	if err != nil {
		return FullResources{}, err
	}
	state, err := s.Load()
	if err != nil {
		return FullResources{}, err
	}
	resources := FullResources{Plan: plan, Payloads: map[tools.ID]FullPayload{}}
	for _, program := range plan.Programs {
		artifact := state.Installed[program.Module].Artifact
		if artifact.Identity() != program.Identity {
			return resources, fmt.Errorf("full resource selection changed; retry")
		}
		payload, err := loadFullPayload(program.Generation, artifact)
		if err != nil {
			return resources, fmt.Errorf("module %s full resources: %w", program.Module, err)
		}
		resources.Payloads[program.Module] = payload
	}
	return resources, nil
}

func (r FullResources) Program(module tools.ID, name string) (string, error) {
	relative, exists := r.Payloads[module].Programs[name]
	if !exists {
		return "", fmt.Errorf("module %s does not declare executable %s", module, name)
	}
	return r.Plan.Program(module, relative)
}

func (r FullResources) Image(module tools.ID, name string) (string, error) {
	image, exists := r.Payloads[module].Images[name]
	if !exists {
		return "", fmt.Errorf("module %s does not declare pinned image %s", module, name)
	}
	return image, nil
}

func (r FullResources) ServiceProgram(module tools.ID, name string) (string, error) {
	relative, exists := r.Payloads[module].Programs[name]
	if !exists {
		return "", fmt.Errorf("module %s does not declare executable %s", module, name)
	}
	return "/opt/loki/modules/" + string(module) + "/" + filepath.ToSlash(relative), nil
}

func (r FullResources) ServiceAsset(module tools.ID, name string) (string, error) {
	relative, exists := r.Payloads[module].Assets[name]
	if !exists {
		return "", fmt.Errorf("module %s does not declare asset %s", module, name)
	}
	return "/opt/loki/modules/" + string(module) + "/" + filepath.ToSlash(relative), nil
}
