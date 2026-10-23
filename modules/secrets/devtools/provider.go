// Package devtools is the finite private CLI adapter for application values.
// It imports neither the old app vault nor coordination or project execution.
package devtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

var ErrUnavailable = errors.New("application value provider unavailable")
var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var id = regexp.MustCompile(`^[0-9a-f]{32}$`)
var profile = regexp.MustCompile(`^app-[0-9a-f]{32}$`)
var key = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

type Provider struct {
	binary, root string
	processOnce  sync.Once
	processes    chan struct{}
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, ErrUnavailable
	}
	return b.Buffer.Write(p)
}

// New verifies the reviewed identity before accessing the isolated app root.
// The binary and root are administrator-selected, never request parameters.
func New(ctx context.Context, binary, root string) (*Provider, error) {
	for _, path := range []string{binary, root} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || strings.ContainsRune(path, 0) {
			return nil, ErrUnavailable
		}
	}
	p := &Provider{binary: binary, root: root}
	var version struct {
		Version  string `json:"version"`
		Commit   string `json:"commit"`
		Protocol int    `json:"protocol_version"`
	}
	if p.invoke(ctx, []string{"version"}, nil, &version) != nil || version.Version != DevtoolsVersion || version.Commit != DevtoolsCommit || version.Protocol != DevtoolsProtocol {
		return nil, ErrUnavailable
	}
	return p, nil
}

// invoke is deliberately private; every caller supplies a compiled operation.
// Neither stderr nor error.details crosses this boundary.
func (p *Provider) invoke(ctx context.Context, args []string, input []byte, target any) error {
	return p.invokeFiles(ctx, args, input, target, nil)
}

// Inherited files are supplied only by typed operations; callers cannot choose
// child descriptors, paths, environment or a generic command.
func (p *Provider) invokeFiles(ctx context.Context, args []string, input []byte, target any, files []*os.File) error {
	if p == nil {
		return ErrUnavailable
	}
	// Eight protected Jobs share this provider inside one 64-PID service.
	// Bound short-lived Go CLI processes independently of live Job capacity;
	// waiting for a slot remains cancellable and creates no external effect.
	p.processOnce.Do(func() { p.processes = make(chan struct{}, 2) })
	select {
	case p.processes <- struct{}{}:
		defer func() { <-p.processes }()
	case <-ctx.Done():
		return ErrUnavailable
	}
	cmd := exec.CommandContext(ctx, p.binary, append(args, "--isolated-root", p.root)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "GOMAXPROCS=2"}
	cmd.Stdin = bytes.NewReader(input)
	cmd.ExtraFiles = files
	output := &boundedBuffer{limit: MaxPrivatePayloadFrame}
	cmd.Stdout = output
	stderr := &boundedBuffer{limit: MaxPrivateControlFrame}
	cmd.Stderr = stderr
	err := cmd.Run()
	defer clear(output.Bytes())
	defer clear(stderr.Bytes())
	if err != nil {
		return providerFailure(stderr.Bytes())
	}
	var envelope struct {
		Schema int             `json:"schema_version"`
		OK     bool            `json:"ok"`
		Data   json.RawMessage `json:"data"`
	}
	if json.Unmarshal(output.Bytes(), &envelope) != nil || envelope.Schema != 1 || !envelope.OK {
		return ErrUnavailable
	}
	defer clear(envelope.Data)
	if json.Unmarshal(envelope.Data, target) != nil {
		return ErrUnavailable
	}
	return nil
}

type RunnerAddress struct {
	RootID            string `json:"root_id"`
	VaultID           string `json:"vault_id"`
	RunnerID          string `json:"runner_id"`
	InstanceID        string `json:"instance_id"`
	Endpoint          string `json:"endpoint"`
	Protocol          int    `json:"protocol"`
	RedactionProtocol int    `json:"redaction_protocol"`
}

func (p *Provider) Address(ctx context.Context, runner, instance string) (RunnerAddress, error) {
	var a RunnerAddress
	if !uuid.MatchString(runner) || !uuid.MatchString(instance) {
		return a, ErrUnavailable
	}
	err := p.invoke(ctx, []string{"values", "runner", "address", runner, "--instance", instance}, nil, &a)
	if err != nil || a.RunnerID != runner || a.InstanceID != instance || a.Protocol != RunnerWireProtocol || a.RedactionProtocol != RedactionProtocol {
		return RunnerAddress{}, ErrUnavailable
	}
	return a, nil
}

func (p *Provider) Register(ctx context.Context, runner, instance, request string) error {
	if !uuid.MatchString(runner) || !uuid.MatchString(instance) || !uuid.MatchString(request) {
		return ErrUnavailable
	}
	body, _ := json.Marshal(struct {
		Runner    string `json:"runner_id"`
		Instance  string `json:"instance_id"`
		Protocol  int    `json:"protocol"`
		Redaction int    `json:"redaction_protocol"`
	}{runner, instance, RunnerWireProtocol, RedactionProtocol})
	var result json.RawMessage
	return p.invoke(ctx, []string{"values", "runner", "register", "--request-id", request, "--stdin"}, body, &result)
}

func (p *Provider) Unregister(ctx context.Context, runner, instance, request string) error {
	if !uuid.MatchString(runner) || !uuid.MatchString(instance) || !uuid.MatchString(request) {
		return ErrUnavailable
	}
	var result struct {
		Item struct {
			Runner   string `json:"runner_id"`
			Instance string `json:"instance_id"`
			Enabled  bool   `json:"enabled"`
		} `json:"item"`
	}
	if err := p.invoke(ctx, []string{"values", "runner", "unregister", runner, "--instance", instance, "--request-id", request}, nil, &result); err != nil {
		return err
	}
	if result.Item.Runner != runner || result.Item.Instance != instance || result.Item.Enabled {
		return ErrUnavailable
	}
	return nil
}

type Execution struct {
	ID             string `json:"execution_id"`
	InputID        string `json:"input_id"`
	JobID          string `json:"external_execution_id"`
	Revision       uint64 `json:"revision"`
	CreatedAt      string `json:"created_at"`
	ExpiresAt      string `json:"expires_at"`
	DeliveryState  string `json:"delivery_state"`
	ExecutionState string `json:"execution_state"`
	RunnerID       string `json:"runner_id,omitempty"`
	InstanceID     string `json:"runner_instance_id,omitempty"`
	ReporterID     string `json:"reporter_instance_id,omitempty"`
	DeliveryID     string `json:"delivery_id,omitempty"`
	ExternalRef    string `json:"external_ref,omitempty"`
	Reason         string `json:"reason,omitempty"`
	FactSource     string `json:"fact_source,omitempty"`
	ExitCode       *int64 `json:"exit_code,omitempty"`
}

func (p *Provider) ExecutionStatus(ctx context.Context, app, execution string) (Execution, error) {
	if !profile.MatchString(app) || !id.MatchString(execution) {
		return Execution{}, ErrUnavailable
	}
	var result struct {
		Item Execution `json:"item"`
	}
	if p.invoke(ctx, []string{"values", "execution", "status", execution, "--profile", app}, nil, &result) != nil || result.Item.ID != execution {
		return Execution{}, ErrUnavailable
	}
	return result.Item, nil
}

func (p *Provider) execution(ctx context.Context, operation, app, execution, request string, input any) (Execution, error) {
	if !profile.MatchString(app) || !uuid.MatchString(request) || execution != "" && !id.MatchString(execution) || !AllowsDevtoolsCommand(DevtoolsJobs, "values execution "+operation) {
		return Execution{}, ErrUnavailable
	}
	body, err := json.Marshal(input)
	if err != nil || len(body) > MaxPrivateControlFrame {
		return Execution{}, ErrUnavailable
	}
	defer clear(body)
	args := []string{"values", "execution", operation}
	if execution != "" {
		args = append(args, execution)
	}
	args = append(args, "--profile", app, "--request-id", request, "--stdin")
	var result struct {
		Profile string    `json:"profile"`
		Item    Execution `json:"item"`
	}
	if err := p.invoke(ctx, args, body, &result); err != nil {
		return Execution{}, err
	}
	if result.Profile != app || !id.MatchString(result.Item.ID) || !id.MatchString(result.Item.InputID) || !id.MatchString(result.Item.JobID) || result.Item.Revision == 0 || execution != "" && result.Item.ID != execution {
		return Execution{}, ErrUnavailable
	}
	return result.Item, nil
}

func (p *Provider) Prepare(ctx context.Context, app, env, job, request, bundle string, keys []string) (Execution, error) {
	if env == "common" {
		env = ""
	}
	if !id.MatchString(job) || len(keys) == 0 || len(keys) > 256 || env != "" && !environment.MatchString(env) {
		return Execution{}, ErrUnavailable
	}
	names := append([]string(nil), keys...)
	sort.Strings(names)
	for i, name := range names {
		if !key.MatchString(name) || i > 0 && name == names[i-1] {
			return Execution{}, ErrUnavailable
		}
	}
	return p.execution(ctx, "prepare", app, "", request, struct {
		Job       string `json:"external_execution_id"`
		Env       string `json:"env,omitempty"`
		Selection struct {
			Keys   []string `json:"keys"`
			Bundle string   `json:"bundle,omitempty"`
		} `json:"selection"`
	}{job, env, struct {
		Keys   []string `json:"keys"`
		Bundle string   `json:"bundle,omitempty"`
	}{names, bundle}})
}

func (p *Provider) Approve(ctx context.Context, app, runner, instance, request string, e Execution) (Execution, error) {
	if !uuid.MatchString(runner) || !uuid.MatchString(instance) || !id.MatchString(e.JobID) || !id.MatchString(e.InputID) || e.Revision == 0 {
		return Execution{}, ErrUnavailable
	}
	return p.execution(ctx, "approve", app, e.ID, request, struct {
		Revision uint64 `json:"revision"`
		Runner   string `json:"runner_id"`
		Instance string `json:"runner_instance_id"`
		Job      string `json:"external_execution_id"`
		Input    string `json:"input_id"`
	}{e.Revision, runner, instance, e.JobID, e.InputID})
}

func (p *Provider) Deliver(ctx context.Context, app, request string, e Execution) (Execution, error) {
	return p.execution(ctx, "deliver", app, e.ID, request, struct {
		Revision uint64 `json:"revision"`
	}{e.Revision})
}

func (p *Provider) Cancel(ctx context.Context, app, request string, e Execution) (Execution, error) {
	return p.execution(ctx, "cancel", app, e.ID, request, struct {
		Revision uint64 `json:"revision"`
	}{e.Revision})
}

type EngineFact struct {
	State       string `json:"state"`
	ExternalRef string `json:"external_ref"`
	ExitCode    *int64 `json:"exit_code,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

func (p *Provider) Report(ctx context.Context, app, request, runner string, e Execution, fact EngineFact) (Execution, error) {
	if !uuid.MatchString(runner) || !id.MatchString(e.JobID) || !id.MatchString(e.InputID) || e.Revision == 0 {
		return Execution{}, ErrUnavailable
	}
	return p.execution(ctx, "report", app, e.ID, request, struct {
		Revision uint64 `json:"revision"`
		Runner   string `json:"runner_id"`
		Job      string `json:"external_execution_id"`
		Input    string `json:"input_id"`
		EngineFact
	}{e.Revision, runner, e.JobID, e.InputID, fact})
}
