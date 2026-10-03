package launcher

import (
	"context"
	"errors"

	"loki/modules/execution/jobs"
)

// The host endpoint relay can resolve only an active lease of this exact
// deployment. EndpointBindings also checks the retained publisher/container
// instance, policy and actual loopback publication through the sandbox engine.
func (l *lifecycle) resolveEndpoint(ctx context.Context, port int) (map[string]any, error) {
	if port < 1024 || port > 65535 {
		return nil, errors.New("invalid managed endpoint port")
	}
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return nil, errors.New("launcher is stopping")
	}
	owner, deployment := l.policy.Deployment()
	if owner == "" || deployment == "" {
		return nil, errors.New("managed host endpoints require their full deployment")
	}
	records, err := l.journal.List()
	if err != nil {
		return nil, err
	}
	var result map[string]any
	for _, record := range records {
		if record.State != jobs.StateRunning || record.InstanceRef == "" {
			continue
		}
		for _, lease := range record.EndpointLeases {
			if lease.HostPort != port || lease.State != jobs.EndpointLeaseActive || lease.JobID != record.ID {
				continue
			}
			resource, err := resourceFromRecord(record)
			if err != nil {
				return nil, err
			}
			currentOwner, currentDeployment := resource.Deployment()
			if currentOwner != owner || currentDeployment != deployment {
				continue
			}
			bindings, err := l.runner.EndpointBindings(ctx, resource, record.InstanceRef, endpointSpecsFromRecord(record))
			if err != nil {
				return nil, err
			}
			matched := false
			for _, binding := range bindings {
				if binding.Name == lease.Name && binding.Port == lease.Port && binding.HostPort == lease.HostPort {
					matched = true
				}
			}
			if !matched || result != nil {
				return nil, errors.New("managed endpoint publication is absent or ambiguous")
			}
			result = map[string]any{"lease": lease.ID, "job_id": record.ID, "port": port}
		}
	}
	if result == nil {
		return nil, errors.New("no active owned endpoint lease")
	}
	return result, nil
}
