package devtools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"loki/internal/policy"
	"loki/internal/process"
)

const defaultMaxOutput = 4 << 20

type Option struct {
	Name       string `json:"name"`
	Boolean    bool   `json:"boolean"`
	Repeatable bool   `json:"repeatable"`
}

type Candidate struct {
	Version          string `json:"version"`
	Commit           string `json:"commit"`
	ProtocolVersion  int    `json:"protocol_version"`
	ApprovedCommands int    `json:"approved_commands"`
	CatalogSHA256    string `json:"catalog_sha256"`
}

type Client struct {
	Binary    string
	CWD       string
	Env       []string
	Timeout   time.Duration
	MaxOutput int
	Identity  *process.Identity
	Workspace *policy.Workspace

	mu        sync.Mutex
	verified  bool
	candidate Candidate
	commands  map[string]compiledCommand
}

type compiledCommand struct {
	command Command
	input   *jsonschema.Resolved
	output  *jsonschema.Resolved
}

type CLIError struct {
	ExitCode int
	Code     string
	Message  string
}

func (e *CLIError) Error() string {
	if e.Code == "" {
		return "devtools command failed"
	}
	return "devtools " + e.Code + ": " + e.Message
}

func NewClient(binary, cwd string, env []string) (*Client, error) {
	if !filepath.IsAbs(binary) {
		return nil, errors.New("devtools binary path must be absolute")
	}
	if !filepath.IsAbs(cwd) {
		return nil, errors.New("devtools working directory must be absolute")
	}
	commands, err := EmbeddedCatalog()
	if err != nil {
		return nil, err
	}
	compiled, err := compileCommands(commands)
	if err != nil {
		return nil, err
	}
	workspace, err := policy.New(cwd)
	if err != nil {
		return nil, err
	}
	client := &Client{
		Binary: binary, CWD: cwd, Env: slices.Clone(env),
		Timeout: 30 * time.Second, MaxOutput: defaultMaxOutput,
		Workspace: workspace, commands: compiled,
	}
	success := false
	defer func() {
		if !success {
			workspace.Close()
		}
	}()
	success = true
	return client, nil
}

func compileCommands(commands []Command) (map[string]compiledCommand, error) {
	compiled := make(map[string]compiledCommand, len(commands))
	for _, command := range commands {
		var schema jsonschema.Schema
		if err := json.Unmarshal(command.InputSchema, &schema); err != nil {
			return nil, fmt.Errorf("decode input schema for %q: %w", command.Name, err)
		}
		input, resolveErr := schema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve input schema for %q: %w", command.Name, resolveErr)
		}
		var outputSchema jsonschema.Schema
		if err := json.Unmarshal(command.OutputSchema, &outputSchema); err != nil {
			return nil, fmt.Errorf("decode output schema for %q: %w", command.Name, err)
		}
		output, resolveErr := outputSchema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve output schema for %q: %w", command.Name, resolveErr)
		}
		compiled[command.Name] = compiledCommand{command: command, input: input, output: output}
	}
	return compiled, nil
}

func (c *Client) Close() error {
	if c.Workspace == nil {
		return nil
	}
	return c.Workspace.Close()
}

func candidateEvidence(version Version, commands []Command) (Candidate, error) {
	raw, err := json.Marshal(commands)
	if err != nil {
		return Candidate{}, err
	}
	sum := sha256.Sum256(raw)
	return Candidate{
		Version: version.Version, Commit: version.Commit, ProtocolVersion: version.ProtocolVersion,
		ApprovedCommands: len(commands), CatalogSHA256: hex.EncodeToString(sum[:]),
	}, nil
}

func (c *Client) Verify(ctx context.Context) (Candidate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.verified {
		return c.candidate, nil
	}
	result, err := process.Run(ctx, process.Spec{
		Argv: []string{c.Binary, "version"}, CWD: c.CWD, Env: c.Env,
		Identity: c.Identity, Timeout: c.timeout(), MaxOutput: 64 << 10,
	})
	if err != nil {
		return Candidate{}, fmt.Errorf("execute devtools version: %w", err)
	}
	if result.TimedOut {
		return Candidate{}, errors.New("devtools version check timed out")
	}
	if result.Truncated || result.ExitCode != 0 {
		return Candidate{}, errors.New("devtools version check failed")
	}
	version, err := ParseVersion(result.Raw)
	if err != nil {
		return Candidate{}, err
	}
	result, err = process.Run(ctx, process.Spec{
		Argv: []string{c.Binary, "schema", "--all"}, CWD: c.CWD, Env: c.Env,
		Identity: c.Identity, Timeout: c.timeout(), MaxOutput: c.maxOutput(),
	})
	if err != nil {
		return Candidate{}, fmt.Errorf("execute devtools schema --all: %w", err)
	}
	if result.TimedOut {
		return Candidate{}, errors.New("devtools schema check timed out")
	}
	if result.Truncated || result.ExitCode != 0 {
		return Candidate{}, errors.New("devtools schema check failed")
	}
	commands, err := ParseCatalog(result.Raw)
	if err != nil {
		return Candidate{}, err
	}
	compiled, err := compileCommands(commands)
	if err != nil {
		return Candidate{}, err
	}
	candidate, err := candidateEvidence(version, commands)
	if err != nil {
		return Candidate{}, errors.New("devtools candidate fingerprint failed")
	}
	c.commands = compiled
	c.candidate = candidate
	c.verified = true
	return candidate, nil
}

func (c *Client) verify(ctx context.Context) error {
	_, err := c.Verify(ctx)
	return err
}

func (c *Client) Call(ctx context.Context, name string, raw json.RawMessage) (json.RawMessage, error) {
	if isMetadataCommand(name) || isAgentGuidanceCommand(name) || isCoordinationCommand(name) || isCoordinationMutation(name) {
		return nil, errors.New("devtools typed command requires the typed adapter")
	}
	return c.call(ctx, name, raw, c.Env)
}

func isMetadataCommand(name string) bool {
	switch name {
	case "project inspect", "command list", "command inspect":
		return true
	default:
		return false
	}
}

func (c *Client) call(ctx context.Context, name string, raw json.RawMessage, environment []string) (json.RawMessage, error) {
	if err := c.verify(ctx); err != nil {
		return nil, err
	}
	command, ok := c.commands[name]
	if !ok {
		return nil, errors.New("devtools command is not approved")
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil || input == nil {
		return nil, errors.New("invalid devtools command input")
	}
	if err := command.input.Validate(input); err != nil {
		return nil, errors.New("devtools command input does not match its schema")
	}
	if (name == "process start" || name == "process restart") && input["capture-logs"] == true {
		return nil, errors.New("devtools raw process logs are disabled")
	}
	if name == "project inspect" {
		if profile, _ := input["profile"].(string); profile != "" {
			return nil, errors.New("devtools explicit profile lookup is unavailable through Loki")
		}
	}
	if name == "process start" || isMetadataCommand(name) || isAgentGuidanceCommand(name) {
		requested, _ := input["dir"].(string)
		if requested == "" {
			requested = "."
		}
		if c.Workspace == nil {
			return nil, errors.New("devtools workspace is unavailable")
		}
		confined, err := c.Workspace.ResolveCWD(requested)
		if err != nil {
			return nil, errors.New("devtools command directory is outside the workspace")
		}
		input["dir"] = confined
	}
	argv, err := buildArgv(command.command, input)
	if err != nil {
		return nil, err
	}
	result, err := process.Run(ctx, process.Spec{
		Argv: append([]string{c.Binary}, argv...), CWD: c.CWD, Env: environment,
		Identity: c.Identity, Timeout: c.timeout(), MaxOutput: c.maxOutput(),
	})
	if err != nil {
		return nil, fmt.Errorf("execute devtools command: %w", err)
	}
	if result.TimedOut {
		return nil, errors.New("devtools command timed out")
	}
	if result.Truncated {
		return nil, errors.New("devtools response exceeded the output limit")
	}
	envelope, err := decodeEnvelope(result.Raw)
	if err != nil {
		return nil, err
	}
	if (result.ExitCode == 0) != envelope.OK {
		return nil, errors.New("devtools exit status contradicts its response envelope")
	}
	if !envelope.OK {
		var failure struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(envelope.Error, &failure)
		return nil, &CLIError{ExitCode: result.ExitCode, Code: failure.Code, Message: failure.Message}
	}
	var output any
	if err = json.Unmarshal(envelope.Data, &output); err != nil || command.output.Validate(output) != nil {
		return nil, errors.New("devtools response does not match its schema")
	}
	return slices.Clone(envelope.Data), nil
}

func (c *Client) timeout() time.Duration {
	if c.Timeout <= 0 {
		return 30 * time.Second
	}
	return c.Timeout
}

func (c *Client) maxOutput() int {
	if c.MaxOutput <= 0 {
		return defaultMaxOutput
	}
	return c.MaxOutput
}

func buildArgv(command Command, input map[string]any) ([]string, error) {
	argv := strings.Fields(command.Name)
	for _, option := range command.Options {
		value, ok := input[option.Name]
		if !ok {
			continue
		}
		flag := "--" + option.Name
		switch typed := value.(type) {
		case bool:
			if typed {
				argv = append(argv, flag)
			}
		case string:
			argv = append(argv, flag, typed)
		case []any:
			if !option.Repeatable {
				return nil, fmt.Errorf("devtools option %q is not repeatable", option.Name)
			}
			for _, item := range typed {
				text, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("devtools option %q must contain strings", option.Name)
				}
				argv = append(argv, flag, text)
			}
		default:
			return nil, fmt.Errorf("unsupported devtools option %q", option.Name)
		}
	}
	if value, ok := input["args"]; ok {
		items, ok := value.([]any)
		if !ok {
			return nil, errors.New("devtools args must be an array")
		}
		for _, item := range items {
			text, ok := item.(string)
			if !ok {
				return nil, errors.New("devtools args must contain strings")
			}
			argv = append(argv, text)
		}
	}
	return argv, nil
}
