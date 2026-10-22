//go:build linux

package admin

import (
	"context"
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
	if privateio.ReadControl(conn, &prepared) != nil {
		return result, privateio.ErrInvalid
	}
	b := prepared.Binding
	if prepared.Stage != "prepared" || b.Operation != operation || b.Attempt != request.RequestID || b.Policy != request.Revision || !slices.Equal(b.Names, request.Members) {
		return result, privateio.ErrInvalid
	}
	if err := persist(b); err != nil {
		return result, err
	}
	ack := appvalues.MigrationApproval{Stage: "approve", Attempt: b.Attempt, Execution: b.Execution, Input: b.Input, Revision: b.Revision}
	if privateio.WriteControl(conn, ack) != nil || privateio.ReadControl(conn, &result) != nil || result.Stage != "matched" || !result.Matched || result.Count != len(expected) {
		return appvalues.MigrationComparisonResult{}, privateio.ErrInvalid
	}
	return result, nil
}
