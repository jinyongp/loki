//go:build linux

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"time"

	"loki/internal/control/appvalues"
	"loki/internal/platform/privateio"
)

// CompareMigration is a two-phase private administrator exchange. The host
// durably checkpoints the broker's immutable binding before sending approval.
// Expected source bytes travel only in the private bounded binary frame.
func (c ApplicationClient) CompareMigration(ctx context.Context, operation string, request appvalues.AdminRequest, expected []privateio.MigrationExpectedValue, persist func(appvalues.MigrationComparisonBinding) error) (appvalues.MigrationComparisonResult, error) {
	var result appvalues.MigrationComparisonResult
	if !filepath.IsAbs(c.Socket) || filepath.Clean(c.Socket) != c.Socket || c.ExpectedUID == 0 || request.Action != "migration_compare" || request.Members != nil || persist == nil {
		return result, privateio.ErrInvalid
	}
	body, err := privateio.EncodeMigrationExpected(expected)
	if err != nil {
		return result, err
	}
	defer clear(body)
	request.Members = make([]string, 0, len(expected))
	for _, value := range expected {
		request.Members = append(request.Members, value.Name)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return result, privateio.ErrInvalid
	}
	defer connection.Close()
	conn, ok := connection.(*net.UnixConn)
	if !ok || privateio.VerifyPeer(conn, c.ExpectedUID) != nil {
		return result, privateio.ErrInvalid
	}
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		return result, privateio.ErrInvalid
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if privateio.WriteControl(conn, request) != nil || privateio.WriteBytes(conn, body, privateio.MaxMigrationFrame) != nil {
		return result, privateio.ErrInvalid
	}
	var prepared appvalues.MigrationPrepared
	first, err := privateio.ReadBytes(conn, privateio.MaxControlFrame)
	if err != nil {
		return result, privateio.ErrInvalid
	}
	defer clear(first)
	var stage struct {
		Stage string `json:"stage"`
	}
	if json.Unmarshal(first, &stage) != nil {
		return result, privateio.ErrInvalid
	}
	if stage.Stage == "failed" {
		var failure appvalues.MigrationComparisonResult
		if privateio.Decode(first, &failure) != nil {
			return result, privateio.ErrInvalid
		}
		return result, migrationFailureError(failure.Reason)
	}
	if privateio.Decode(first, &prepared) != nil {
		return result, fmt.Errorf("%w: migration prepared schema", privateio.ErrInvalid)
	}
	b := prepared.Binding
	if prepared.Stage != "prepared" {
		return result, fmt.Errorf("%w: migration prepared stage", privateio.ErrInvalid)
	}
	if b.Operation != operation {
		return result, fmt.Errorf("%w: migration operation binding", privateio.ErrInvalid)
	}
	if b.Attempt != request.RequestID {
		return result, fmt.Errorf("%w: migration attempt binding", privateio.ErrInvalid)
	}
	if b.Policy != request.Revision {
		return result, fmt.Errorf("%w: migration policy binding", privateio.ErrInvalid)
	}
	if !slices.Equal(b.Names, request.Members) {
		return result, fmt.Errorf("%w: migration selection binding", privateio.ErrInvalid)
	}
	if err := persist(b); err != nil {
		return result, err
	}
	ack := appvalues.MigrationApproval{Stage: "approve", Attempt: b.Attempt, Execution: b.Execution, Input: b.Input, Revision: b.Revision}
	if privateio.WriteControl(conn, ack) != nil || privateio.ReadControl(conn, &result) != nil {
		return appvalues.MigrationComparisonResult{}, fmt.Errorf("%w: migration result frame", privateio.ErrInvalid)
	}
	if result.Stage == "failed" {
		return appvalues.MigrationComparisonResult{}, migrationFailureError(result.Reason)
	}
	if result.Stage != "matched" || !result.Matched || result.Count != len(expected) {
		return appvalues.MigrationComparisonResult{}, privateio.ErrInvalid
	}
	return result, nil
}

func migrationFailureError(reason string) error {
	switch reason {
	case "input", "policy", "environment", "values", "address", "prepare", "listener", "register", "approve", "deliver", "receipt", "unregister", "checkpoint":
		return fmt.Errorf("%w: migration %s", privateio.ErrInvalid, reason)
	default:
		return privateio.ErrInvalid
	}
}
