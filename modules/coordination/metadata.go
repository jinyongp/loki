package devtools

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type ProjectMetadata struct {
	Profile    string `json:"profile"`
	Source     string `json:"source"`
	Root       string `json:"root"`
	ConfigPath string `json:"config_path"`
}

type CommandSummary struct {
	Name   string   `json:"name"`
	Exec   []string `json:"exec"`
	Inject bool     `json:"inject"`
	Env    string   `json:"env"`
	Serve  []string `json:"serve"`
}

type CommandCatalog struct {
	Profile string           `json:"profile"`
	Items   []CommandSummary `json:"items"`
}

type CommandBinding struct {
	Proxy    string  `json:"proxy,omitempty"`
	Format   string  `json:"format,omitempty"`
	Port     string  `json:"port,omitempty"`
	Profile  string  `json:"profile,omitempty"`
	Instance string  `json:"instance,omitempty"`
	Template *string `json:"template,omitempty"`
}

type CommandReady struct {
	Exec    []string `json:"exec"`
	Timeout string   `json:"timeout"`
}

type CommandToolRequirement struct {
	Executable  string   `json:"executable"`
	Version     string   `json:"version"`
	VersionArgs []string `json:"version_args"`
}

type CommandRequirements struct {
	Tools map[string]CommandToolRequirement `json:"tools"`
	Vars  []string                          `json:"vars"`
	Secs  []string                          `json:"secs"`
}

type CommandMetadata struct {
	Name         string                    `json:"name"`
	Exec         []string                  `json:"exec"`
	Inject       bool                      `json:"inject"`
	Env          string                    `json:"env"`
	Serve        []string                  `json:"serve"`
	Bind         map[string]CommandBinding `json:"bind"`
	Requirements CommandRequirements       `json:"requirements"`
	Ready        *CommandReady             `json:"ready"`
}

type CommandDetail struct {
	Profile string          `json:"profile"`
	Item    CommandMetadata `json:"item"`
}

func (c *Client) InspectProject(ctx context.Context, directory string) (ProjectMetadata, error) {
	raw, err := c.metadataCall(ctx, "project inspect", directory, map[string]any{})
	if err != nil {
		return ProjectMetadata{}, err
	}
	var response struct {
		Item struct {
			Profile    string `json:"profile"`
			Source     string `json:"source"`
			ConfigPath string `json:"config_path"`
			Root       string `json:"root"`
		} `json:"item"`
		Paths struct {
			Config string `json:"config"`
			Data   string `json:"data"`
			Cache  string `json:"cache"`
		} `json:"paths"`
	}
	if err := decodeObject(raw, &response); err != nil {
		return ProjectMetadata{}, errors.New("devtools returned invalid project metadata")
	}
	if response.Item.Profile == "" || response.Item.Source != "file" || response.Item.Root == "" || response.Item.ConfigPath == "" {
		return ProjectMetadata{}, errors.New("devtools returned incomplete project metadata")
	}
	root, err := c.workspaceRelative(response.Item.Root, true)
	if err != nil {
		return ProjectMetadata{}, errors.New("devtools project root is outside the workspace")
	}
	config, err := c.workspaceRelative(response.Item.ConfigPath, false)
	if err != nil {
		return ProjectMetadata{}, errors.New("devtools project configuration is outside the workspace")
	}
	return ProjectMetadata{Profile: response.Item.Profile, Source: response.Item.Source, Root: root, ConfigPath: config}, nil
}

func (c *Client) ListCommands(ctx context.Context, directory string) (CommandCatalog, error) {
	result := CommandCatalog{Items: []CommandSummary{}}
	input := map[string]any{}
	seen := map[string]bool{}
	for {
		raw, err := c.metadataCall(ctx, "command list", directory, input)
		if err != nil {
			return CommandCatalog{}, err
		}
		var page struct {
			Profile     string            `json:"profile"`
			Items       []json.RawMessage `json:"items"`
			NextCursor  string            `json:"next_cursor"`
			NextActions json.RawMessage   `json:"next_actions"`
			Page        json.RawMessage   `json:"page"`
			Selection   json.RawMessage   `json:"selection"`
		}
		if err := decodeObject(raw, &page); err != nil || page.Profile == "" || page.Items == nil ||
			(result.Profile != "" && result.Profile != page.Profile) {
			return CommandCatalog{}, errors.New("devtools returned invalid command catalog")
		}
		result.Profile = page.Profile
		for _, item := range page.Items {
			var summary CommandSummary
			if err := json.Unmarshal(item, &summary); err != nil {
				return CommandCatalog{}, errors.New("devtools returned invalid command metadata")
			}
			result.Items = append(result.Items, summary)
		}
		if page.NextCursor == "" {
			return result, nil
		}
		if seen[page.NextCursor] {
			return CommandCatalog{}, errors.New("devtools repeated a command catalog cursor")
		}
		seen[page.NextCursor] = true
		input["cursor"] = page.NextCursor
	}
}

func (c *Client) InspectCommand(ctx context.Context, directory, name string) (CommandDetail, error) {
	if name == "" {
		return CommandDetail{}, errors.New("devtools command name is required")
	}
	raw, err := c.metadataCall(ctx, "command inspect", directory, map[string]any{"args": []string{name}})
	if err != nil {
		return CommandDetail{}, err
	}
	var result CommandDetail
	var wire struct {
		Profile string          `json:"profile"`
		Item    json.RawMessage `json:"item"`
		Values  json.RawMessage `json:"values"`
	}
	if err := decodeObject(raw, &wire); err != nil || wire.Profile == "" ||
		json.Unmarshal(wire.Item, &result.Item) != nil || result.Item.Name == "" {
		return CommandDetail{}, errors.New("devtools returned invalid command metadata")
	}
	result.Profile = wire.Profile
	if result.Item.Name != name {
		return CommandDetail{}, errors.New("devtools returned metadata for a different command")
	}
	return result, nil
}

func (c *Client) metadataCall(ctx context.Context, command, directory string, input map[string]any) (json.RawMessage, error) {
	if directory == "" {
		directory = "."
	}
	if err := c.requireWorkspaceProject(directory); err != nil {
		return nil, err
	}
	input["dir"] = directory
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, errors.New("encode devtools metadata request")
	}
	return c.call(ctx, command, raw, c.Env)
}

func (c *Client) workspaceRelative(target string, directory bool) (string, error) {
	if c.Workspace == nil || !filepath.IsAbs(target) {
		return "", errors.New("workspace path is unavailable")
	}
	relative, err := filepath.Rel(c.Workspace.Root(), target)
	if err != nil {
		return "", err
	}
	relative = filepath.ToSlash(relative)
	if relative == "" {
		relative = "."
	}
	if strings.HasPrefix(relative, "../") || relative == ".." {
		return "", errors.New("path escapes workspace")
	}
	var resolved string
	if directory {
		resolved, err = c.Workspace.ResolveCWD(relative)
	} else {
		resolved, err = c.Workspace.Resolve(relative, true)
	}
	if err != nil || filepath.Clean(resolved) != filepath.Clean(target) {
		return "", errors.New("path does not resolve inside workspace")
	}
	return relative, nil
}

func (c *Client) requireWorkspaceProject(directory string) error {
	if c.Workspace == nil {
		return errors.New("devtools workspace is unavailable")
	}
	confined, err := c.Workspace.ResolveCWD(directory)
	if err != nil {
		return errors.New("devtools command directory is outside the workspace")
	}
	relative, err := filepath.Rel(c.Workspace.Root(), confined)
	if err != nil {
		return errors.New("devtools command directory is outside the workspace")
	}
	for {
		candidate := filepath.Join(relative, "devtools.toml")
		file, openErr := c.Workspace.Open(filepath.ToSlash(candidate), os.O_RDONLY, 0)
		if openErr == nil {
			info, statErr := file.Stat()
			file.Close()
			if statErr != nil || !info.Mode().IsRegular() {
				return errors.New("devtools project configuration is unavailable")
			}
			return nil
		}
		if !errors.Is(openErr, fs.ErrNotExist) {
			return errors.New("devtools project configuration is unavailable")
		}
		if relative == "." {
			break
		}
		parent := filepath.Dir(relative)
		if parent == relative {
			break
		}
		relative = parent
	}
	return errors.New("devtools project configuration is not present in the workspace")
}
