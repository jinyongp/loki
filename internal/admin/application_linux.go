//go:build linux

package admin

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"time"

	"loki/internal/control/appvalues"
	"loki/internal/fault"
	"loki/internal/platform/privateio"
)

// ApplicationClient is the administrator-only binary-input channel. ExpectedUID
// is obtained from the installed deployment contract, never a public request.
type ApplicationClient struct {
	Socket      string
	ExpectedUID uint32
}

func (c ApplicationClient) Call(ctx context.Context, q appvalues.AdminRequest, body []byte) (json.RawMessage, error) {
	if !filepath.IsAbs(c.Socket) || filepath.Clean(c.Socket) != c.Socket || c.ExpectedUID == 0 || len(body) > 500000 {
		return nil, privateio.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return nil, fault.New(fault.CodeUnavailable, "application administration is unavailable", true, "inspect the secret service")
	}
	defer conn.Close()
	unixConn, ok := conn.(*net.UnixConn)
	if !ok || privateio.VerifyPeer(unixConn, c.ExpectedUID) != nil {
		return nil, privateio.ErrInvalid
	}
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if privateio.WriteControl(conn, q) != nil {
		return nil, privateio.ErrInvalid
	}
	if limit := q.InputLimit(); limit > 0 {
		if privateio.WriteBytes(conn, body, limit) != nil {
			return nil, privateio.ErrInvalid
		}
	} else if len(body) != 0 {
		return nil, privateio.ErrInvalid
	}
	// Result objects may contain null public metadata; strict private decoding
	// applies to the envelope's controls rather than nullable public output.
	raw, err := privateio.ReadBytes(conn, privateio.MaxControlFrame)
	if err != nil {
		return nil, privateio.ErrInvalid
	}
	defer clear(raw)
	var response struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  *fault.Detail   `json:"error"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return nil, privateio.ErrInvalid
	}
	if !response.OK {
		if response.Error == nil || !fault.ValidCode(response.Error.Code) {
			return nil, privateio.ErrInvalid
		}
		d := response.Error
		return nil, fault.New(d.Code, d.Message, d.Retryable, d.NextAction)
	}
	if response.Error != nil || len(response.Result) == 0 {
		return nil, privateio.ErrInvalid
	}
	return response.Result, nil
}
