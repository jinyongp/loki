// Package deployment defines the service topology shared by container
// packaging and native service adapters.
package deployment

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"slices"
)

const Version = 1

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var modePattern = regexp.MustCompile(`^[0-7]{4}$`)

type Contract struct {
	Version       int                `json:"version"`
	Platforms     []string           `json:"platforms"`
	Architectures []string           `json:"container_architectures"`
	Images        map[string]Image   `json:"images"`
	Services      map[string]Service `json:"services"`
	Volumes       map[string]Volume  `json:"volumes"`
	Sockets       map[string]Socket  `json:"sockets"`
	Networks      map[string]Network `json:"networks"`
	Features      map[string]Feature `json:"features"`
}

type Image struct {
	Roles    []string `json:"roles"`
	Optional bool     `json:"optional"`
}

type Service struct {
	Image           string   `json:"image"`
	Role            string   `json:"role"`
	User            string   `json:"user"`
	Required        bool     `json:"required"`
	Profile         string   `json:"profile,omitempty"`
	VaultAccess     bool     `json:"vault_access"`
	WorkspaceAccess string   `json:"workspace_access"`
	Networks        []string `json:"networks"`
	ReadOnlyRoot    bool     `json:"read_only_root"`
	NoNewPrivileges bool     `json:"no_new_privileges"`
	MemoryMB        int      `json:"memory_mb"`
	PIDs            int      `json:"pids"`
}

type Volume struct {
	Path       string `json:"path"`
	Owner      string `json:"owner"`
	Group      string `json:"group"`
	Mode       string `json:"mode"`
	Persistent bool   `json:"persistent"`
	External   bool   `json:"external"`
}

type Socket struct {
	Path    string   `json:"path"`
	Owner   string   `json:"owner"`
	Clients []string `json:"clients"`
}

type Network struct {
	Internal bool     `json:"internal"`
	Services []string `json:"services"`
}

type Feature struct {
	Activation string   `json:"activation"`
	Default    bool     `json:"default"`
	Services   []string `json:"services"`
}

func Load(raw []byte) (Contract, error) {
	var contract Contract
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&contract); err != nil {
		return Contract{}, fmt.Errorf("decode deployment contract: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Contract{}, errors.New("deployment contract contains trailing data")
	}
	if err := contract.Validate(); err != nil {
		return Contract{}, err
	}
	return contract, nil
}

func (c Contract) Validate() error {
	if c.Version != Version {
		return fmt.Errorf("unsupported deployment contract version %d", c.Version)
	}
	if !slices.Equal(c.Platforms, []string{"linux-container", "linux-systemd"}) ||
		!slices.Equal(c.Architectures, []string{"amd64", "arm64"}) {
		return errors.New("deployment platforms are incomplete")
	}
	if len(c.Images) != 2 || c.Images["loki"].Optional || !c.Images["loki-browser"].Optional {
		return errors.New("deployment images are invalid")
	}
	for name, image := range c.Images {
		if !validName(name) || !sortedNames(image.Roles) || len(image.Roles) == 0 {
			return fmt.Errorf("invalid image %q", name)
		}
	}
	for _, name := range []string{"egress", "executor", "launcher", "mcp", "runtime"} {
		if service, ok := c.Services[name]; !ok || !service.Required {
			return fmt.Errorf("required service %q is missing", name)
		}
	}
	for name, service := range c.Services {
		image, ok := c.Images[service.Image]
		if !validName(name) || !ok || !slices.Contains(image.Roles, service.Role) || !validName(service.User) {
			return fmt.Errorf("invalid service %q", name)
		}
		if service.Required == (service.Profile != "") || service.Profile != "" && !validName(service.Profile) {
			return fmt.Errorf("service %q profile is invalid", name)
		}
		if service.VaultAccess != (name == "runtime") {
			return fmt.Errorf("service %q has invalid vault access", name)
		}
		if !slices.Contains([]string{"none", "read", "write"}, service.WorkspaceAccess) || !sortedNames(service.Networks) {
			return fmt.Errorf("service %q access policy is invalid", name)
		}
		if !service.ReadOnlyRoot || !service.NoNewPrivileges || service.MemoryMB < 32 || service.PIDs < 16 {
			return fmt.Errorf("service %q resource policy is incomplete", name)
		}
		if slices.Contains(service.Networks, "outbound") != (name == "egress" || name == "browser-proxy") {
			return fmt.Errorf("service %q has invalid outbound access", name)
		}
		for _, network := range service.Networks {
			if !slices.Contains(c.Networks[network].Services, name) {
				return fmt.Errorf("service %q network membership is inconsistent", name)
			}
		}
	}
	paths := map[string]string{}
	for name, volume := range c.Volumes {
		if !validName(name) || !filepath.IsAbs(volume.Path) || filepath.Clean(volume.Path) != volume.Path ||
			!validName(volume.Owner) || !validName(volume.Group) || !modePattern.MatchString(volume.Mode) {
			return fmt.Errorf("invalid volume %q", name)
		}
		if other := paths[volume.Path]; other != "" {
			return fmt.Errorf("volumes %q and %q share a path", other, name)
		}
		paths[volume.Path] = name
		if volume.External != (name == "workspace") || name == "sockets" && volume.Persistent {
			return fmt.Errorf("volume %q lifecycle is invalid", name)
		}
	}
	paths = map[string]string{}
	for name, socket := range c.Sockets {
		if !validName(name) || !filepath.IsAbs(socket.Path) || filepath.Clean(socket.Path) != socket.Path || paths[socket.Path] != "" {
			return fmt.Errorf("invalid socket %q", name)
		}
		paths[socket.Path] = name
		if _, ok := c.Services[socket.Owner]; !ok || !sortedNames(socket.Clients) || len(socket.Clients) == 0 {
			return fmt.Errorf("socket %q ownership is invalid", name)
		}
		for _, client := range socket.Clients {
			if _, ok := c.Services[client]; !ok || client == socket.Owner {
				return fmt.Errorf("socket %q has invalid client", name)
			}
		}
	}
	if len(c.Networks) != 2 || !c.Networks["private"].Internal || c.Networks["outbound"].Internal {
		return errors.New("deployment networks are incomplete")
	}
	for name, network := range c.Networks {
		if !validName(name) || !sortedNames(network.Services) {
			return fmt.Errorf("invalid network %q", name)
		}
		for _, member := range network.Services {
			service, ok := c.Services[member]
			if !ok || !slices.Contains(service.Networks, name) {
				return fmt.Errorf("network %q membership is inconsistent", name)
			}
		}
	}
	for _, name := range []string{"browser", "docker", "github", "signing"} {
		feature, ok := c.Features[name]
		if !ok || feature.Default || !slices.Contains([]string{"config", "profile"}, feature.Activation) || !sortedNames(feature.Services) {
			return fmt.Errorf("optional feature %q is invalid", name)
		}
		for _, member := range feature.Services {
			service, ok := c.Services[member]
			if !ok {
				return fmt.Errorf("feature %q references unknown service", name)
			}
			if feature.Activation == "profile" && service.Profile != name {
				return fmt.Errorf("feature %q service %q uses another profile", name, member)
			}
		}
	}
	if c.Features["browser"].Activation != "profile" || !slices.Equal(c.Features["browser"].Services, []string{"browser", "browser-proxy"}) ||
		c.Features["signing"].Activation != "profile" || !slices.Equal(c.Features["signing"].Services, []string{"signing"}) ||
		c.Features["github"].Activation != "config" || !slices.Equal(c.Features["github"].Services, []string{"egress", "runtime"}) ||
		c.Features["docker"].Activation != "config" || !slices.Equal(c.Features["docker"].Services, []string{"launcher"}) {
		return errors.New("optional feature topology is invalid")
	}
	return nil
}

func validName(value string) bool { return namePattern.MatchString(value) }

func sortedNames(values []string) bool {
	if !slices.IsSorted(values) {
		return false
	}
	for index, value := range values {
		if !validName(value) || index > 0 && value == values[index-1] {
			return false
		}
	}
	return true
}
