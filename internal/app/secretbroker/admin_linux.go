//go:build linux

package secretbroker

import (
	"context"
	"net"
	"sync"
	"time"

	"loki/internal/fault"
	"loki/internal/platform/privateio"
	"loki/modules/secrets/application"
)

type adminResponse struct {
	OK     bool           `json:"ok"`
	Result map[string]any `json:"result,omitempty"`
	Error  *fault.Detail  `json:"error,omitempty"`
}

type AdminOptions struct {
	Socket string
	Values *application.Service
	Ready  func() error
}

// ServeAdmin authenticates the kernel UID before accepting either frame. The
// second frame is private input and is erased after the one operation ends.
func ServeAdmin(ctx context.Context, options AdminOptions) error {
	values := options.Values
	if values == nil {
		return errWire
	}
	l, err := listenPrivate(options.Socket)
	if err != nil {
		return err
	}
	defer l.Close()
	if options.Ready != nil {
		if err := options.Ready(); err != nil {
			return err
		}
	}
	stop := context.AfterFunc(ctx, func() { l.Close() })
	defer stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	slots := make(chan struct{}, 4)
	for {
		conn, err := l.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if privateio.VerifyPeer(conn, 0) != nil {
			conn.Close()
			continue
		}
		select {
		case slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		wg.Go(func() { defer func() { <-slots }(); defer conn.Close(); serveAdmin(ctx, conn, values) })
	}
}

func serveAdmin(ctx context.Context, conn *net.UnixConn, values *application.Service) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	var request application.AdminRequest
	if privateio.ReadControl(conn, &request) != nil {
		return
	}
	var body []byte
	if limit := request.InputLimit(); limit > 0 {
		var err error
		// Explicit empty=true distinguishes an intentional empty secret from
		// a disconnected private writer without inventing a payload byte.
		body, err = privateio.ReadBytes(conn, limit)
		if err != nil {
			return
		}
		defer clear(body)
	}
	out, err := values.Admin(ctx, request, body)
	r := adminResponse{OK: err == nil, Result: out}
	if err != nil {
		d := fault.Describe(err)
		r.Error = &d
		r.Result = nil
	}
	_ = privateio.WriteControl(conn, r)
}
