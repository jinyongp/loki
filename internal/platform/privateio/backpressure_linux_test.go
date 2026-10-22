//go:build linux

package privateio

import (
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type heldConsumer struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	bytes   int
}

func (w *heldConsumer) Write(body []byte) (int, error) {
	w.once.Do(func() { close(w.entered); <-w.release })
	w.bytes += len(body)
	return len(body), nil
}

func TestSlowConsumerBackpressuresBoundedUnixStreamWithoutLosingEOF(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	connections := make([]*net.UnixConn, 0, 2)
	for _, fd := range fds {
		file := os.NewFile(uintptr(fd), "bounded-stream")
		conn, err := net.FileConn(file)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn.(*net.UnixConn))
		defer conn.Close()
	}
	sink := &heldConsumer{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-sink.release:
		default:
			close(sink.release)
		}
	})
	session := &JobSession{conn: connections[0]}
	collected := make(chan error, 1)
	go func() { collected <- session.Collect(sink) }()
	produced := make(chan error, 1)
	go func() {
		frame := make([]byte, (64<<10)+1)
		frame[0] = StreamStdout
		for i := 0; i < 32; i++ {
			if err := WriteBytes(connections[1], frame, len(frame)); err != nil {
				produced <- err
				return
			}
		}
		produced <- WriteBytes(connections[1], []byte{StreamEOF}, 1)
	}()
	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		t.Fatal("consumer did not receive first chunk")
	}
	select {
	case err := <-produced:
		close(sink.release)
		t.Fatal("producer bypassed blocked consumer", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(sink.release)
	select {
	case err := <-produced:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("producer did not resume")
	}
	select {
	case err := <-collected:
		if err != nil || sink.bytes != 32*(64<<10) {
			t.Fatal("output or EOF lost", err, sink.bytes)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("collector did not observe EOF")
	}
}
