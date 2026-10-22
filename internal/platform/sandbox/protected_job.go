package sandbox

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"loki/internal/platform/privateio"
)

// ProtectedAttachment keeps raw bytes in the launcher-to-broker channel.
// No method stores them or falls back to Docker logs.
type ProtectedAttachment struct {
	engine            *Engine
	resource          Resource
	instance, version string
	body              io.ReadWriteCloser
	base              []string
	once              sync.Once
}

func (a *ProtectedAttachment) Close() error                { return a.body.Close() }
func (a *ProtectedAttachment) Write(p []byte) (int, error) { return a.body.Write(p) }
func (a *ProtectedAttachment) BootstrapEnvironment(plan Plan) ([]privateio.EnvironmentEntry, error) {
	if !plan.Valid() || !plan.protected || plan.Resource() != a.resource {
		return nil, errors.New("private bootstrap plan changed")
	}
	result := []privateio.EnvironmentEntry{}
	for _, entry := range plan.create.Env {
		name, value, _ := strings.Cut(entry, "=")
		result = append(result, privateio.EnvironmentEntry{Name: name, Value: value})
	}
	if plan.network == NetworkApplication {
		for _, name := range []string{"HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy"} {
			found := false
			for _, entry := range a.base {
				key, value, _ := strings.Cut(entry, "=")
				if key == name {
					result = append(result, privateio.EnvironmentEntry{Name: name, Value: value})
					found = true
					break
				}
			}
			if !found {
				return nil, errors.New("authenticated application routing unavailable")
			}
		}
	}
	return result, nil
}

// Streams keeps independent stream identity across arbitrary Docker frame
// boundaries. Bounded synchronous writes apply backpressure without raw spools.
func (a *ProtectedAttachment) Streams(stdout, stderr io.Writer) error {
	var header [8]byte
	var chunk [64 << 10]byte
	defer clear(chunk[:])
	for {
		_, err := io.ReadFull(a.body, header[:])
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil || header[0] < 1 || header[0] > 2 || header[1] != 0 || header[2] != 0 || header[3] != 0 {
			return errors.New("private Docker output interrupted")
		}
		remaining := int64(binary.BigEndian.Uint32(header[4:]))
		out := stdout
		if header[0] == 2 {
			out = stderr
		}
		for remaining > 0 {
			n := int(min(remaining, int64(len(chunk))))
			if _, err = io.ReadFull(a.body, chunk[:n]); err != nil {
				return errors.New("private Docker output interrupted")
			}
			written, err := out.Write(chunk[:n])
			clear(chunk[:n])
			if err != nil || written != n {
				return errors.New("private Docker collector interrupted")
			}
			remaining -= int64(n)
		}
	}
}

// PrepareProtectedJob completes cold image/domain preparation while leaving
// the workload stopped. The launcher must durably bind InstanceRef before attach.
func (e *Engine) PrepareProtectedJob(ctx context.Context, plan Plan) (StartResult, error) {
	if e == nil || !plan.Valid() || !plan.protected {
		return StartResult{}, errors.New("invalid protected workload plan")
	}
	version, err := e.apiVersion(ctx)
	if err != nil {
		return StartResult{}, err
	}
	if plan.NeedsGateway() {
		return e.startDomainJob(ctx, version, plan)
	}
	id, created, err := e.create(ctx, version, plan)
	result := StartResult{Resource: plan.Resource(), InstanceRef: instanceReference(id), Created: created}
	if err != nil {
		return result, err
	}
	if result.InstanceRef == "" {
		return result, errors.New("protected instance identity unavailable")
	}
	return result, nil
}

func (e *Engine) AttachProtectedJob(ctx context.Context, resource Resource, instance string) (*ProtectedAttachment, error) {
	if e == nil || !resource.Valid() || !validInstanceReference(instance) {
		return nil, errors.New("invalid private attach identity")
	}
	version, err := e.apiVersion(ctx)
	if err != nil {
		return nil, err
	}
	owned, err := e.resolveProtectedWorkload(ctx, version, resource, instance)
	if err != nil {
		return nil, err
	}
	if !owned.state.Exists || owned.state.Running || owned.state.Terminal || owned.logType != "none" || owned.logPath != "" {
		return nil, errors.New("protected workload is not stopped without persistent logging")
	}
	endpoint := "/v" + version + "/containers/" + url.PathEscape(owned.id) + "/attach?stream=1&stdin=1&stdout=1&stderr=1&logs=0"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker"+endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "tcp")
	response, err := e.client.Do(request)
	if err != nil {
		return nil, errors.New("protected workload attach unavailable")
	}
	if response.StatusCode != http.StatusSwitchingProtocols || !strings.EqualFold(response.Header.Get("Upgrade"), "tcp") {
		response.Body.Close()
		return nil, errors.New("protected workload attach refused")
	}
	body, ok := response.Body.(io.ReadWriteCloser)
	if !ok {
		response.Body.Close()
		return nil, errors.New("protected workload attach is not duplex")
	}
	return &ProtectedAttachment{engine: e, resource: resource, instance: instance, version: version, body: body, base: owned.environment}, nil
}

// Start runs once, after private bootstrap delivery has been authorized. No
// connection recovery can attach to or start a replacement container.
func (a *ProtectedAttachment) Start(ctx context.Context) error {
	err := errors.New("protected workload start was already attempted")
	a.once.Do(func() {
		owned, lookup := a.engine.resolveProtectedWorkload(ctx, a.version, a.resource, a.instance)
		if lookup != nil {
			err = lookup
			return
		}
		if !owned.state.Exists || owned.state.Running || owned.state.Terminal || owned.logType != "none" || owned.logPath != "" {
			err = errors.New("protected workload identity or logging changed")
			return
		}
		err = a.engine.startRef(ctx, a.version, owned.id, a.resource)
		if err != nil {
			return
		}
		started, inspectErr := a.engine.resolveExactWorkload(ctx, a.version, a.resource, a.instance, true)
		if inspectErr != nil {
			err = inspectErr
			return
		}
		if !started.state.Exists || !started.state.Running && !started.state.Terminal || started.logType != "none" || started.logPath != "" {
			err = errors.New("protected workload start was not observed")
		}
	})
	return err
}

func (e *Engine) resolveProtectedWorkload(ctx context.Context, version string, resource Resource, instance string) (inspectedResource, error) {
	if !isDomainInstanceReference(instance) {
		return e.resolveExactWorkload(ctx, version, resource, instance, true)
	}
	reference, ok := parseDomainReference(instance)
	if !ok {
		return inspectedResource{}, ErrInstanceMismatch
	}
	snapshot, err := e.inspectDomain(ctx, version, resource)
	if err != nil {
		return inspectedResource{}, err
	}
	if err = reference.preparedCompletenessError(snapshot); err != nil {
		return inspectedResource{}, err
	}
	return snapshot.workload, nil
}
