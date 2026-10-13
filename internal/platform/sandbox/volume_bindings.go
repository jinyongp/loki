package sandbox

import (
	"errors"
	"path/filepath"
	"sort"
	"strings"
)

// Volume mappings are administrator policy, never workload input. Keep logical
// paths available to the launcher while giving Docker its owned named volumes,
// rather than bind mounting the daemon's internal volume directories.
func normalizeVolumeBindings(values []VolumeBinding, owner, workspace, input, toolchains string) ([]VolumeBinding, error) {
	if len(values) > 3 {
		return nil, errors.New("sandbox has too many owned volume bindings")
	}
	bindings := append([]VolumeBinding(nil), values...)
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].Path < bindings[j].Path })
	names := map[string]bool{}
	for index, binding := range bindings {
		if !cleanAbsoluteNonRoot(binding.Path) || !volumeNamePattern.MatchString(binding.Name) || !strings.HasPrefix(binding.Name, owner+"-data-") || names[binding.Name] {
			return nil, errors.New("sandbox volume binding leaves its deployment owner")
		}
		used := binding.Path == workspace || binding.Path == toolchains || input != "" && insideVolume(binding.Path, input)
		if !used {
			return nil, errors.New("sandbox volume binding is outside declared execution paths")
		}
		for _, other := range bindings[:index] {
			if insideVolume(other.Path, binding.Path) || insideVolume(binding.Path, other.Path) {
				return nil, errors.New("sandbox volume bindings overlap")
			}
		}
		names[binding.Name] = true
	}
	return bindings, nil
}

func insideVolume(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func (p Policy) volumeMount(mount dockerMount) dockerMount {
	if mount.Type != "bind" {
		return mount
	}
	for _, binding := range p.volumes {
		if !insideVolume(binding.Path, mount.Source) {
			continue
		}
		relative, _ := filepath.Rel(binding.Path, mount.Source)
		if relative == "." {
			relative = ""
		}
		return dockerMount{Type: "volume", Source: binding.Name, Target: mount.Target, ReadOnly: mount.ReadOnly, VolumeOptions: &dockerVolumeOptions{NoCopy: true, Subpath: filepath.ToSlash(relative)}}
	}
	return mount
}
