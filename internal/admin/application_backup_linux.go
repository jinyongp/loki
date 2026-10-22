//go:build linux

package admin

import (
	"context"
	"encoding/hex"

	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/platform/privateio"
)

// ExportApplication obtains a metadata-only proof. The host copies the finite
// encrypted artifact from the owned generation; this channel never returns
// ciphertext payloads, decrypted values or arbitrary provider filesystem paths.
func (c ApplicationClient) ExportApplication(ctx context.Context, q appvalues.AdminRequest, resource, policy string) (appvalues.ApplicationExportProof, error) {
	var result struct {
		Export appvalues.ApplicationExportProof `json:"export"`
	}
	if q.Action != "backup_export" || !controlpolicy.ApplicationUUID.MatchString(resource) {
		return result.Export, privateio.ErrInvalid
	}
	body, err := c.Call(ctx, q, nil)
	if err != nil {
		return result.Export, err
	}
	if privateio.Decode(body, &result) != nil {
		return appvalues.ApplicationExportProof{}, privateio.ErrInvalid
	}
	p := result.Export
	sum, err := hex.DecodeString(p.CipherSHA256)
	if err != nil || len(sum) != 32 || p.CipherSHA256 != hex.EncodeToString(sum) || p.Version != 1 || p.Operation != q.RequestID || p.Resource != resource || p.Policy != policy || p.Revision != q.Revision || p.Recipient != q.Recipient || p.Bytes < 1 || p.Bytes > 64<<20 {
		return appvalues.ApplicationExportProof{}, privateio.ErrInvalid
	}
	return p, nil
}
