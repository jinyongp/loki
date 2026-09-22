package mcptransport

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	"loki/internal/audit"
)

type toolActivityRecord struct {
	Timestamp    string         `json:"timestamp"`
	Phase        string         `json:"phase"`
	InvocationID string         `json:"invocation_id"`
	Tool         string         `json:"tool"`
	Success      bool           `json:"success"`
	Outcome      string         `json:"outcome"`
	DurationMS   float64        `json:"duration_ms"`
	Metadata     map[string]any `json:"metadata"`
	SessionRef   string         `json:"session_ref"`
}

type toolActivityItem struct {
	InvocationID string         `json:"invocation_id"`
	Tool         string         `json:"tool"`
	State        string         `json:"state"`
	StartedAt    string         `json:"started_at,omitempty"`
	TerminalAt   string         `json:"terminal_at,omitempty"`
	Success      *bool          `json:"success,omitempty"`
	Outcome      string         `json:"outcome,omitempty"`
	DurationMS   *float64       `json:"duration_ms,omitempty"`
	AgeSeconds   *float64       `json:"age_seconds,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	SessionRef   string         `json:"session_ref,omitempty"`
	sortTime     time.Time
}

func activityTimestamp(value string) time.Time {
	timestamp, _ := time.Parse("2006-01-02T15:04:05.000000+00:00", value)
	return timestamp
}

func isActivitySelfRecord(record toolActivityRecord) bool {
	if record.Tool != "system_inspect" || record.Metadata == nil {
		return false
	}
	action := record.Metadata["action"]
	return action == "activity" || action == "operation"
}

func retainedToolActivity(log *audit.Log, now time.Time) ([]toolActivityItem, error) {
	if log == nil {
		return nil, errors.New("tool activity log is unavailable")
	}
	raw, err := log.Read(200)
	if err != nil {
		return nil, err
	}
	rows, ok := raw["records"].([]json.RawMessage)
	if !ok {
		return nil, errors.New("invalid audit activity records")
	}
	byID := map[string]*toolActivityItem{}
	for _, row := range rows {
		var record toolActivityRecord
		if err := json.Unmarshal(row, &record); err != nil || record.InvocationID == "" || (record.Phase != "start" && record.Phase != "terminal") {
			continue
		}
		if isActivitySelfRecord(record) {
			continue
		}
		item := byID[record.InvocationID]
		if item == nil {
			item = &toolActivityItem{InvocationID: record.InvocationID, Tool: record.Tool, State: "started"}
			byID[record.InvocationID] = item
		}
		timestamp := activityTimestamp(record.Timestamp)
		if timestamp.After(item.sortTime) {
			item.sortTime = timestamp
		}
		if item.Tool == "" {
			item.Tool = record.Tool
		}
		if item.SessionRef == "" {
			item.SessionRef = record.SessionRef
		}
		if record.Phase == "terminal" {
			success := record.Success
			duration := record.DurationMS
			item.State = "terminal"
			item.TerminalAt = record.Timestamp
			item.Success = &success
			item.Outcome = record.Outcome
			item.DurationMS = &duration
			if record.Metadata != nil {
				item.Metadata = record.Metadata
			}
			continue
		}
		item.StartedAt = record.Timestamp
		if item.Metadata == nil && record.Metadata != nil {
			item.Metadata = record.Metadata
		}
	}
	items := make([]toolActivityItem, 0, len(byID))
	for _, item := range byID {
		if item.State == "started" && item.StartedAt != "" {
			started := activityTimestamp(item.StartedAt)
			if !started.IsZero() {
				age := now.Sub(started).Seconds()
				if age < 0 {
					age = 0
				}
				item.AgeSeconds = &age
			}
		}
		items = append(items, *item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].sortTime.Equal(items[j].sortTime) {
			return items[i].InvocationID > items[j].InvocationID
		}
		return items[i].sortTime.After(items[j].sortTime)
	})
	for index := range items {
		items[index].sortTime = time.Time{}
	}
	return items, nil
}

func recentToolActivity(log *audit.Log, limit int, now time.Time) ([]toolActivityItem, error) {
	if limit < 1 || limit > 50 {
		return nil, errors.New("tool activity limit must be between 1 and 50")
	}
	items, err := retainedToolActivity(log, now)
	if err != nil {
		return nil, err
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func toolActivityByCorrelationID(log *audit.Log, correlationID string, now time.Time) (toolActivityItem, error) {
	items, err := retainedToolActivity(log, now)
	if err != nil {
		return toolActivityItem{}, err
	}
	for _, item := range items {
		if item.InvocationID == correlationID {
			return item, nil
		}
	}
	return toolActivityItem{}, errors.New("tool activity correlation is not retained")
}
