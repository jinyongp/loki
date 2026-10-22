//go:build linux

package admin

import (
	"context"
	"encoding/json"

	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/platform/privateio"
)

// FenceApplicationWrites verifies the exact root-only generation gate ACK.
// A completed begin replay is not permission to perform another host effect.
func (c ApplicationClient) FenceApplicationWrites(ctx context.Context, operation string, active bool) error {
	if !controlpolicy.ApplicationUUID.MatchString(operation) {
		return privateio.ErrInvalid
	}
	action := "maintenance_end"
	if active {
		action = "maintenance_begin"
	}
	body, err := c.Call(ctx, appvalues.AdminRequest{Action: action, RequestID: operation}, nil)
	if err != nil {
		return err
	}
	var result struct {
		Maintenance struct {
			Version   int    `json:"version"`
			Operation string `json:"operation_id"`
			Active    bool   `json:"active"`
		} `json:"maintenance"`
	}
	if json.Unmarshal(body, &result) != nil || result.Maintenance.Version != 1 || result.Maintenance.Operation != operation || result.Maintenance.Active != active {
		return privateio.ErrInvalid
	}
	return nil
}
