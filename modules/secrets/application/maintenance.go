//go:build linux

package application

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"loki/internal/fault"
	"loki/internal/platform/privateio"
	"loki/internal/platform/safeio"
)

// valueMaintenance is a subordinate write gate, not another host operation
// journal. Only the root administrator may change it; canonical host progress
// decides admission/release. It remains beside this generation's app metadata
// across broker restart and selector cutover, independently of key sessions.
type valueMaintenance struct {
	Version   int    `json:"version"`
	Operation string `json:"operation_id"`
	Active    bool   `json:"active"`
}

func (s *Service) maintenanceState() (*valueMaintenance, error) {
	body, err := readPrivate(filepath.Join(s.options.Root, "maintenance.json"), 4096)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, unavailable()
	}
	defer clear(body)
	var state valueMaintenance
	if privateio.Decode(body, &state) != nil || state.Version != 1 || !adminUUID.MatchString(state.Operation) {
		return nil, unavailable()
	}
	return &state, nil
}

func (s *Service) requireWritable() error {
	state, err := s.maintenanceState()
	if err != nil {
		return err
	}
	if state != nil && state.Active {
		return fault.New(fault.CodeUnavailable, "application value writes are fenced by host maintenance", true, "resume the original host operation")
	}
	return nil
}

func (s *Service) maintenance(q AdminRequest, body []byte) (map[string]any, error) {
	if len(body) != 0 || q.Resource != "" || q.Environment != "" || q.Principal != "" || q.Variables != nil || q.Members != nil || q.Revision != "" || q.Name != "" || q.Empty || q.Replace {
		return nil, invalid()
	}
	action := strings.TrimPrefix(q.Action, "maintenance_")
	if action == "status" {
		if q.RequestID != "" {
			return nil, invalid()
		}
	} else if action != "begin" && action != "end" || !adminUUID.MatchString(q.RequestID) {
		return nil, invalid()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.maintenanceState()
	if err != nil {
		return nil, err
	}
	if action == "status" {
		return map[string]any{"maintenance": state}, nil
	}
	if state != nil && state.Operation != q.RequestID && state.Active || action == "end" && (state == nil || state.Operation != q.RequestID) {
		return nil, conflict()
	}
	// Replaying a completed begin never reopens its gate. Only the canonical
	// host owner can admit a different operation after completion.
	if state != nil && state.Operation == q.RequestID && (action == "begin" || !state.Active) {
		return map[string]any{"maintenance": *state}, nil
	}
	state = &valueMaintenance{Version: 1, Operation: q.RequestID, Active: action == "begin"}
	encoded, _ := json.Marshal(state)
	if safeio.PublishPrivate(filepath.Join(s.options.Root, "maintenance.json"), encoded, true) != nil {
		return nil, unavailable()
	}
	return map[string]any{"maintenance": *state}, nil
}
