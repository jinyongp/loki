// Package signing exposes identity lookup and signing from a private SSH agent.
package signing

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"loki/internal/rpc"
)

const MaxMessage = 1024 * 1024

var failure = []byte{0, 0, 0, 1, 5}

type Proxy struct {
	PrivateSocket       string
	RunnerUID, AgentUID uint32
	MaxConnections      int
	Timeout             time.Duration
}

func frame(reader io.Reader, allowEmpty bool) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size > MaxMessage || (size == 0 && !allowEmpty) {
		return nil, errors.New("invalid SSH agent frame size")
	}
	data := make([]byte, 4+int(size))
	copy(data, header[:])
	if _, err := io.ReadFull(reader, data[4:]); err != nil {
		return nil, err
	}
	return data, nil
}
func write(conn net.Conn, data []byte) error {
	for len(data) > 0 {
		n, err := conn.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func (p Proxy) Serve(ctx context.Context, listener *net.UnixListener) error {
	maximum := p.MaxConnections
	if maximum == 0 {
		maximum = 32
	}
	if maximum < 1 || maximum > 256 {
		return errors.New("invalid signing connection limit")
	}
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	var clients sync.WaitGroup
	defer clients.Wait()
	slots := make(chan struct{}, maximum)
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
			clients.Go(func() { defer func() { <-slots }(); defer conn.Close(); p.handle(ctx, conn) })
		default:
			conn.Close()
		}
	}
}

func (p Proxy) handle(ctx context.Context, conn *net.UnixConn) {
	peer, err := rpc.PeerCredentials(conn)
	if err != nil || peer.UID != p.RunnerUID {
		return
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	dialer := net.Dialer{Timeout: timeout}
	upstream, err := dialer.DialContext(ctx, "unix", p.PrivateSocket)
	if err != nil {
		return
	}
	defer upstream.Close()
	stopUpstream := context.AfterFunc(ctx, func() { upstream.Close() })
	defer stopUpstream()
	agent, err := rpc.PeerCredentials(upstream.(*net.UnixConn))
	if err != nil || agent.UID != p.AgentUID {
		return
	}
	for {
		if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
			return
		}
		request, err := frame(conn, false)
		if err != nil {
			return
		}
		if request[4] != 11 && request[4] != 13 {
			if err := write(conn, failure); err != nil {
				return
			}
			continue
		}
		if err := upstream.SetDeadline(time.Now().Add(timeout)); err != nil {
			return
		}
		if err := write(upstream, request); err != nil {
			return
		}
		response, err := frame(upstream, true)
		if err != nil {
			return
		}
		if err := write(conn, response); err != nil {
			return
		}
	}
}
