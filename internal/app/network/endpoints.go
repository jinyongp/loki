package network

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"loki/internal/rpc"
)

// EndpointDialer reaches host loopback through a protected Unix relay. The
// four-byte request carries only a port; it cannot name arbitrary hosts/files.
type EndpointDialer struct {
	Socket string
	UID    uint32
}

func (d EndpointDialer) Dial(ctx context.Context, port int) (net.Conn, error) {
	if !filepath.IsAbs(d.Socket) || filepath.Clean(d.Socket) != d.Socket {
		return nil, errors.New("endpoint relay requires a clean absolute socket")
	}
	if port != 0 && (port < 1024 || port > 65535) {
		return nil, errors.New("invalid managed endpoint port")
	}
	connection, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", d.Socket)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			connection.Close()
		}
	}()
	peer, err := rpc.PeerCredentials(connection.(*net.UnixConn))
	if err != nil || peer.UID != d.UID {
		return nil, errors.New("unexpected protected endpoint peer")
	}
	deadline := time.Now().Add(5 * time.Second)
	if requested, ok := ctx.Deadline(); ok && requested.Before(deadline) {
		deadline = requested
	}
	connection.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	defer stop()
	var request [4]byte
	binary.BigEndian.PutUint32(request[:], uint32(port))
	if _, err := connection.Write(request[:]); err != nil {
		return nil, err
	}
	var response [1]byte
	if _, err := io.ReadFull(connection, response[:]); err != nil || response[0] != 0 {
		return nil, errors.New("managed endpoint is unavailable or disabled")
	}
	connection.SetDeadline(time.Time{})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	success = true
	return connection, nil
}

// RunOwnedEndpoints is a host-network role with a private Unix listener. It
// owns no Docker socket, credentials or workspace. Validate returns the exact
// lease identity only after protected launcher publication checks. Connections
// close when activation or the lease changes, including existing tunnels.
func RunOwnedEndpoints(ctx context.Context, listener *net.UnixListener, clients []uint32, validate func(context.Context, int) (string, error), enabled func() error, ready func() error) error {
	if validate == nil || enabled == nil || len(clients) == 0 {
		return errors.New("owned endpoint policy is required")
	}
	if err := enabled(); err != nil {
		return err
	}
	if ready != nil {
		if err := ready(); err != nil {
			return err
		}
	}
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	var workers sync.WaitGroup
	defer workers.Wait()
	slots := make(chan struct{}, 32)
	for {
		connection, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			connection.Close()
			continue
		}
		workers.Go(func() {
			defer func() { <-slots }()
			defer connection.Close()
			stopClient := context.AfterFunc(ctx, func() { connection.Close() })
			defer stopClient()
			peer, err := rpc.PeerCredentials(connection)
			if err != nil {
				return
			}
			allowed := false
			for _, uid := range clients {
				allowed = allowed || uid == peer.UID
			}
			if !allowed {
				return
			}
			connection.SetDeadline(time.Now().Add(5 * time.Second))
			var request [4]byte
			if _, err := io.ReadFull(connection, request[:]); err != nil || enabled() != nil {
				return
			}
			port := int(binary.BigEndian.Uint32(request[:]))
			if port == 0 {
				connection.Write([]byte{0})
				return
			}
			if port < 1024 || port > 65535 {
				return
			}
			check := func() (string, error) {
				bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
				return validate(bounded, port)
			}
			lease, err := check()
			if err != nil || lease == "" {
				return
			}
			upstream, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", "127.0.0.1:"+strconv.Itoa(port))
			if err != nil {
				return
			}
			defer upstream.Close()
			current, err := check()
			if err != nil || current != lease || enabled() != nil {
				return
			}
			if _, err := connection.Write([]byte{0}); err != nil {
				return
			}
			connection.SetDeadline(time.Time{})
			relayContext, cancel := context.WithCancel(ctx)
			defer cancel()
			stopRelay := context.AfterFunc(relayContext, func() { connection.Close(); upstream.Close() })
			defer stopRelay()
			watchDone := make(chan struct{})
			go func() {
				defer close(watchDone)
				ticker := time.NewTicker(time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-relayContext.Done():
						return
					case <-ticker.C:
						current, err := check()
						if err != nil || current != lease || enabled() != nil {
							cancel()
							return
						}
					}
				}
			}()
			done := make(chan struct{}, 2)
			go func() { io.Copy(upstream, connection); done <- struct{}{} }()
			go func() { io.Copy(connection, upstream); done <- struct{}{} }()
			<-done
			cancel()
			<-done
			<-watchDone
		})
	}
}
