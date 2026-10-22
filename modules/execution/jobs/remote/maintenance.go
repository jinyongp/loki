package remote

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"loki/internal/rpc"
	"loki/modules/execution/jobs"
)

// HostMaintenance talks only to the root-owned launcher. The launcher checks
// the caller's kernel UID; neither the agent nor the executor has this grant.
type HostMaintenance struct{ client rpc.Client }

func NewHostMaintenance(socket string) (*HostMaintenance, error) {
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket || socket == "/" || strings.ContainsRune(socket, 0) {
		return nil, errors.New("invalid host maintenance socket")
	}
	root := uint32(0)
	return &HostMaintenance{client: rpc.Client{Socket: socket, ExpectedUID: &root, Limits: rpc.Limits{RequestBytes: 4096, ResponseBytes: 4096, Timeout: 30 * time.Second}}}, nil
}

func (c *HostMaintenance) Begin(ctx context.Context, operation string, interrupt bool) (jobs.Maintenance, error) {
	if _, err := jobs.JobIDForRequestID(operation); err != nil || operation != strings.ToLower(operation) {
		return jobs.Maintenance{}, errors.New("invalid host operation UUID")
	}
	raw, err := c.client.Call(ctx, struct {
		Operation string `json:"operation"`
		ID        string `json:"operation_id"`
		Interrupt bool   `json:"interrupt"`
	}{"maintenance_begin", operation, interrupt})
	if err != nil {
		return jobs.Maintenance{}, err
	}
	var state jobs.Maintenance
	if json.Unmarshal(raw, &state) != nil || state.Version != 1 || state.OperationID != operation || state.Interrupt != interrupt {
		return jobs.Maintenance{}, errors.New("invalid maintenance response")
	}
	return state, nil
}

func (c *HostMaintenance) End(ctx context.Context, operation string) (jobs.Maintenance, error) {
	if _, err := jobs.JobIDForRequestID(operation); err != nil || operation != strings.ToLower(operation) {
		return jobs.Maintenance{}, errors.New("invalid host operation UUID")
	}
	raw, err := c.client.Call(ctx, struct {
		Operation string `json:"operation"`
		ID        string `json:"operation_id"`
	}{"maintenance_end", operation})
	if err != nil {
		return jobs.Maintenance{}, err
	}
	var state jobs.Maintenance
	if json.Unmarshal(raw, &state) != nil || state.Version != 1 || state.OperationID != operation || state.Active {
		return jobs.Maintenance{}, errors.New("invalid maintenance response")
	}
	return state, nil
}

func (c *HostMaintenance) Status(ctx context.Context) (*jobs.Maintenance, error) {
	raw, err := c.client.Call(ctx, struct {
		Operation string `json:"operation"`
	}{"maintenance_status"})
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 1 || len(fields["maintenance"]) == 0 {
		return nil, errors.New("invalid maintenance response")
	}
	var result *jobs.Maintenance
	if json.Unmarshal(fields["maintenance"], &result) != nil {
		return nil, errors.New("invalid maintenance response")
	}
	if state := result; state != nil {
		if _, err := jobs.JobIDForRequestID(state.OperationID); err != nil || state.Version != 1 || state.OperationID != strings.ToLower(state.OperationID) {
			return nil, errors.New("invalid maintenance response")
		}
	}
	return result, nil
}
