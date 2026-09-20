package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	dockerRawStreamHeaderBytes = 8
	instanceRefPrefix          = "oci-instance-sha256:"
)

type EndpointBinding struct {
	Name     string
	Port     int
	HostPort int
}

type StartResult struct {
	Resource         Resource
	InstanceRef      string
	EndpointBindings []EndpointBinding
	Created          bool
	Started          bool
}

func instanceReference(containerID string) string {
	if !containerIDPattern.MatchString(containerID) {
		return ""
	}
	sum := sha256.Sum256([]byte(containerID))
	return instanceRefPrefix + hex.EncodeToString(sum[:])
}

func validInstanceReference(value string) bool {
	digest, ok := strings.CutPrefix(value, instanceRefPrefix)
	if ok && containerIDPattern.MatchString(digest) {
		return true
	}
	return validDomainInstanceReference(value)
}

func instanceMatches(containerID, reference string) bool {
	expected := instanceReference(containerID)
	return expected != "" && expected == reference
}

func (e *Engine) StartJob(ctx context.Context, plan Plan) (StartResult, error) {
	if e == nil || !plan.Valid() {
		return StartResult{}, errors.New("sandbox workload plan is invalid")
	}
	resource := plan.Resource()
	result := StartResult{Resource: resource}
	version, err := e.apiVersion(ctx)
	if err != nil {
		return result, err
	}
	if plan.NeedsGateway() {
		return e.startDomainJob(ctx, version, plan)
	}
	containerID, created, err := e.create(ctx, version, plan)
	result.Created = created
	result.InstanceRef = instanceReference(containerID)
	if err != nil {
		return result, err
	}
	if result.InstanceRef == "" {
		return result, errors.New("sandbox Docker daemon did not provide an exact instance identity")
	}
	if err = e.startRef(ctx, version, containerID, resource); err != nil {
		return result, err
	}
	result.Started = true

	inspected, err := e.inspectRef(context.Background(), version, containerID, resource)
	if err != nil {
		return result, err
	}
	if !inspected.state.Exists || inspected.id != containerID {
		return result, errors.New("sandbox resource identity changed after start")
	}
	return result, nil
}

func (e *Engine) resolveExactWorkload(
	ctx context.Context, version string, resource Resource, instanceRef string, requireDomainComplete bool,
) (inspectedResource, error) {
	if isDomainInstanceReference(instanceRef) {
		return e.resolveDomainWorkload(ctx, version, resource, instanceRef, requireDomainComplete)
	}
	owned, err := e.inspectRef(ctx, version, resource.Name(), resource)
	if err != nil {
		return inspectedResource{}, err
	}
	if !owned.state.Exists {
		return inspectedResource{}, nil
	}
	if !instanceMatches(owned.id, instanceRef) {
		return inspectedResource{}, ErrInstanceMismatch
	}
	return owned, nil
}

func (e *Engine) InspectJob(ctx context.Context, resource Resource, instanceRef string) (ResourceState, error) {
	if e == nil || !resource.Valid() || !validInstanceReference(instanceRef) {
		return ResourceState{}, errors.New("sandbox exact resource inspection is not configured")
	}
	version, err := e.apiVersion(ctx)
	if err != nil {
		return ResourceState{}, err
	}
	owned, err := e.resolveExactWorkload(ctx, version, resource, instanceRef, true)
	if err != nil {
		return ResourceState{}, err
	}
	return owned.state, nil
}

func (e *Engine) OutputJob(ctx context.Context, resource Resource, instanceRef string) ([]byte, bool, error) {
	if e == nil || !resource.Valid() || !validInstanceReference(instanceRef) {
		return nil, false, errors.New("sandbox exact output is not configured")
	}
	version, err := e.apiVersion(ctx)
	if err != nil {
		return nil, false, err
	}
	owned, err := e.resolveExactWorkload(ctx, version, resource, instanceRef, true)
	if err != nil {
		return nil, false, err
	}
	if !owned.state.Exists {
		return nil, false, errors.New("sandbox workload output is unavailable")
	}
	return e.readLogs(version, owned.id)
}

func (e *Engine) ObserveJob(ctx context.Context, resource Resource, instanceRef string) (Result, error) {
	if e == nil || !resource.Valid() || !validInstanceReference(instanceRef) {
		return Result{Outcome: OutcomeUnknown, Cleanup: CleanupFailed}, errors.New("sandbox exact resource observation is not configured")
	}
	version, err := e.apiVersion(ctx)
	if err != nil {
		return Result{Outcome: OutcomeUnknown, Cleanup: CleanupPending}, err
	}
	owned, err := e.resolveExactWorkload(ctx, version, resource, instanceRef, true)
	if err != nil {
		cleanup := CleanupPending
		if errors.Is(err, ErrInstanceMismatch) {
			cleanup = CleanupFailed
		}
		return Result{Outcome: OutcomeUnknown, Cleanup: cleanup}, err
	}
	if !owned.state.Exists {
		return Result{Outcome: OutcomeUnknown, Cleanup: CleanupComplete}, nil
	}
	if owned.state.Terminal {
		return e.resultFromTerminal(version, owned)
	}
	if !owned.state.Running {
		return Result{Outcome: OutcomeUnknown, Cleanup: CleanupPending}, nil
	}

	exitCode, waitErr := e.waitRef(ctx, version, owned.id, resource)
	if waitErr != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			terminal, terminateErr := e.terminateForObservation(version, resource, owned.id)
			if terminateErr != nil {
				return Result{Outcome: outcomeForError(ctx.Err()), Cleanup: CleanupFailed}, errors.Join(ctx.Err(), terminateErr)
			}
			if !terminal.state.Exists {
				return Result{Outcome: outcomeForError(ctx.Err()), Cleanup: CleanupPending}, nil
			}
			if !terminal.state.Terminal {
				return Result{Outcome: outcomeForError(ctx.Err()), Cleanup: CleanupFailed}, errors.New("sandbox workload remained nonterminal after cancellation")
			}
			result, resultErr := e.resultFromTerminal(version, terminal)
			if resultErr != nil {
				return Result{Outcome: outcomeForError(ctx.Err()), Cleanup: CleanupFailed}, resultErr
			}
			if result.Outcome != OutcomeOOMKilled {
				result.Outcome = outcomeForError(ctx.Err())
			}
			return result, nil
		}

		observed, inspectErr := e.resolveExactWorkload(
			context.Background(), version, resource, instanceRef, true,
		)
		if inspectErr == nil && observed.state.Exists && observed.id == owned.id && observed.state.Terminal {
			return e.resultFromTerminal(version, observed)
		}
		return Result{Outcome: OutcomeUnknown, Cleanup: CleanupPending}, errors.Join(waitErr, inspectErr)
	}

	terminal, err := e.resolveExactWorkload(
		context.Background(), version, resource, instanceRef, true,
	)
	if err != nil {
		return Result{ExitCode: exitCode, ExitCodeKnown: true, Outcome: OutcomeUnknown, Cleanup: CleanupPending}, err
	}
	if !terminal.state.Exists || terminal.id != owned.id || !terminal.state.Terminal || terminal.state.ExitCode != exitCode {
		return Result{ExitCode: exitCode, ExitCodeKnown: true, Outcome: OutcomeUnknown, Cleanup: CleanupPending}, errors.New("sandbox Docker daemon returned contradictory terminal state")
	}
	return e.resultFromTerminal(version, terminal)
}

func (e *Engine) CleanupJob(ctx context.Context, resource Resource, instanceRef string) (CleanupStatus, error) {
	if e == nil || !resource.Valid() || !validInstanceReference(instanceRef) {
		return CleanupFailed, errors.New("sandbox exact cleanup is not configured")
	}
	version, err := e.apiVersion(ctx)
	if err != nil {
		return CleanupFailed, err
	}
	if isDomainInstanceReference(instanceRef) {
		return e.cleanupDomain(version, resource, instanceRef)
	}
	owned, err := e.inspectRef(ctx, version, resource.Name(), resource)
	if err != nil {
		return CleanupFailed, err
	}
	if !owned.state.Exists {
		return CleanupComplete, nil
	}
	if !instanceMatches(owned.id, instanceRef) {
		return CleanupFailed, ErrInstanceMismatch
	}
	return e.cleanupResource(version, resource, owned.id)
}

func (e *Engine) resultFromTerminal(version string, inspected inspectedResource) (Result, error) {
	if !inspected.state.Exists || !inspected.state.Terminal || inspected.state.Running {
		return Result{Outcome: OutcomeUnknown, Cleanup: CleanupPending}, errors.New("sandbox resource is not terminal")
	}
	output, truncated, err := e.readLogs(version, inspected.id)
	if err != nil {
		return Result{
			ExitCode:        inspected.state.ExitCode,
			ExitCodeKnown:   true,
			Outcome:         OutcomeUnknown,
			Output:          output,
			OutputTruncated: truncated,
			Cleanup:         CleanupPending,
		}, err
	}
	outcome := OutcomeExited
	if inspected.state.OOMKilled {
		outcome = OutcomeOOMKilled
	}
	return Result{
		ExitCode:        inspected.state.ExitCode,
		ExitCodeKnown:   true,
		Outcome:         outcome,
		Output:          output,
		OutputTruncated: truncated,
		Cleanup:         CleanupPending,
	}, nil
}

func (e *Engine) terminateForObservation(version string, resource Resource, expectedID string) (inspectedResource, error) {
	owned, err := e.inspectRef(context.Background(), version, resource.Name(), resource)
	if err != nil {
		return inspectedResource{}, err
	}
	if !owned.state.Exists {
		return owned, nil
	}
	if expectedID != "" && owned.id != expectedID {
		return inspectedResource{}, ErrInstanceMismatch
	}
	ownedID := owned.id
	if !owned.state.Running {
		return owned, nil
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), e.gracefulStopTimeout+e.controlTimeout)
	stopErr := e.stopRef(stopCtx, version, ownedID, resource)
	cancel()

	postStop, inspectErr := e.inspectRef(context.Background(), version, ownedID, resource)
	if inspectErr != nil {
		return inspectedResource{}, errors.Join(stopErr, inspectErr)
	}
	if !postStop.state.Exists {
		return postStop, nil
	}
	if postStop.id != ownedID {
		return inspectedResource{}, errors.Join(stopErr, ErrInstanceMismatch)
	}
	if !postStop.state.Running {
		return postStop, nil
	}

	killCtx, killCancel := context.WithTimeout(context.Background(), e.cleanupTimeout)
	killErr := e.killRef(killCtx, version, ownedID, resource)
	killCancel()
	if killErr != nil {
		return inspectedResource{}, errors.Join(stopErr, killErr)
	}
	postKill, inspectErr := e.inspectRef(context.Background(), version, ownedID, resource)
	if inspectErr != nil {
		return inspectedResource{}, inspectErr
	}
	if !postKill.state.Exists {
		return postKill, nil
	}
	if postKill.id != ownedID {
		return inspectedResource{}, ErrInstanceMismatch
	}
	if postKill.state.Running {
		return postKill, errors.New("sandbox resource remained running after forced cancellation")
	}
	return postKill, nil
}

func (e *Engine) readLogs(version, ref string) ([]byte, bool, error) {
	if !containerIDPattern.MatchString(ref) {
		return nil, false, errors.New("sandbox exact resource identity is invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), e.controlTimeout)
	defer cancel()
	endpoint := "/v" + version + "/containers/" + url.PathEscape(ref) + "/logs?stderr=1&stdout=1"
	response, err := e.call(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, false, e.unexpectedStatus(response)
	}
	return readDockerRawStream(response.Body, e.outputBytes)
}

func readDockerRawStream(reader io.Reader, maximum int) ([]byte, bool, error) {
	if maximum < 1 || maximum > 16<<20 {
		return nil, false, errors.New("sandbox output limit is outside the supported range")
	}
	var output bytes.Buffer
	output.Grow(min(maximum, 64<<10))
	var header [dockerRawStreamHeaderBytes]byte
	for {
		_, err := io.ReadFull(reader, header[:])
		if errors.Is(err, io.EOF) {
			return output.Bytes(), false, nil
		}
		if err != nil {
			return nil, false, errors.New("sandbox Docker log stream is truncated")
		}
		if (header[0] != 1 && header[0] != 2) || header[1] != 0 || header[2] != 0 || header[3] != 0 {
			return nil, false, errors.New("sandbox Docker log stream has an invalid frame header")
		}
		size := int64(binary.BigEndian.Uint32(header[4:]))
		remaining := maximum - output.Len()
		if size > int64(remaining) {
			if remaining > 0 {
				if _, err = io.CopyN(&output, reader, int64(remaining)); err != nil {
					return nil, false, errors.New("sandbox Docker log stream is truncated")
				}
			}
			return output.Bytes(), true, nil
		}
		if size == 0 {
			continue
		}
		if _, err = io.CopyN(&output, reader, size); err != nil {
			return nil, false, errors.New("sandbox Docker log stream is truncated")
		}
	}
}
