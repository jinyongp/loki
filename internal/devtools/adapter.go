package devtools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"loki/internal/process"
)

const defaultMaxOutput = 4 << 20

type Option struct {
	Name       string `json:"name"`
	Boolean    bool   `json:"boolean"`
	Repeatable bool   `json:"repeatable"`
}

type Client struct {
	Binary    string
	CWD       string
	Env       []string
	Timeout   time.Duration
	MaxOutput int
	Identity  *process.Identity

	mu       sync.Mutex
	verified bool
	commands map[string]compiledCommand
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
	client := &Client{
		Binary: binary, CWD: cwd, Env: slices.Clone(env),
		Timeout: 30 * time.Second, MaxOutput: defaultMaxOutput,
		commands: make(map[string]compiledCommand, len(commands)),
	}
	for _, command := range commands {
		var schema jsonschema.Schema
		if err = json.Unmarshal(command.InputSchema, &schema); err != nil {
			return nil, fmt.Errorf("decode input schema for %q: %w", command.Name, err)
		}
		input, resolveErr := schema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve input schema for %q: %w", command.Name, resolveErr)
		}
		var outputSchema jsonschema.Schema
		if err = json.Unmarshal(command.OutputSchema, &outputSchema); err != nil {
			return nil, fmt.Errorf("decode output schema for %q: %w", command.Name, err)
		}
		output, resolveErr := outputSchema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve output schema for %q: %w", command.Name, resolveErr)
		}
		client.commands[command.Name] = compiledCommand{command: command, input: input, output: output}
	}
	return client, nil
}

func (c *Client) verify(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.verified {
		return nil
	}
	result, err := process.Run(ctx, process.Spec{
		Argv: []string{c.Binary, "version"}, CWD: c.CWD, Env: c.Env,
		Identity: c.Identity, Timeout: c.timeout(), MaxOutput: 64 << 10,
	})
	if err != nil {
		return fmt.Errorf("execute devtools version: %w", err)
	}
	if result.TimedOut {
		return errors.New("devtools version check timed out")
	}
	if result.Truncated || result.ExitCode != 0 {
		return errors.New("devtools version check failed")
	}
	if _, err = ParseVersion(result.Raw); err != nil {
		return err
	}
	c.verified = true
	return nil
}

func (c *Client) Call(ctx context.Context, name string, raw json.RawMessage) (json.RawMessage, error) {
	return c.call(ctx, name, raw, c.Env)
}

func (c *Client) call(ctx context.Context, name string, raw json.RawMessage, environment []string) (json.RawMessage, error) {
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
	argv, err := buildArgv(command.command, input)
	if err != nil {
		return nil, err
	}
	if err = c.verify(ctx); err != nil {
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
	var envelope Envelope
	if err = json.Unmarshal(result.Raw, &envelope); err != nil {
		return nil, errors.New("devtools returned an invalid response")
	}
	if result.ExitCode != 0 || !envelope.OK {
		var failure struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(envelope.Error, &failure)
		return nil, &CLIError{ExitCode: result.ExitCode, Code: failure.Code, Message: failure.Message}
	}
	if envelope.SchemaVersion != ProtocolVersion || len(envelope.Data) == 0 || len(envelope.Error) != 0 {
		return nil, errors.New("devtools returned an unsupported response")
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
