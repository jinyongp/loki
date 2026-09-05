package dockerproxy

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixture(t *testing.T) (Config, *net.UnixListener) {
	t.Helper()
	root, err := os.MkdirTemp("", "loki-docker-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	upstream := filepath.Join(root, "upstream.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: upstream, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return Config{Directory: root, Upstream: upstream, RunnerUID: uint32(os.Getuid()), RunnerGID: uint32(os.Getgid()), UpstreamUID: uint32(os.Getuid())}, listener
}
func TestSessionRelayHalfCloseAndCleanup(t *testing.T) {
	config, upstream := fixture(t)
	session, err := Start(config)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	fd, err := session.Socket()
	if err != nil {
		t.Fatal(err)
	}
	fd.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := upstream.AcceptUnix()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		data, err := io.ReadAll(conn)
		if err == nil {
			_, err = conn.Write(append([]byte("reply:"), data...))
		}
		done <- err
	}()
	client, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: session.Path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := client.Write([]byte("GET /_ping")); err != nil {
		t.Fatal(err)
	}
	client.CloseWrite()
	data, err := io.ReadAll(client)
	if err != nil || string(data) != "reply:GET /_ping" {
		t.Fatalf("%s %v", data, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(session.Path)); !os.IsNotExist(err) {
		t.Fatal("proxy session survived cleanup")
	}
}
func TestSessionRejectsUIDAndClosesActiveConnections(t *testing.T) {
	for _, mode := range []string{"client-uid", "upstream-uid", "close"} {
		t.Run(mode, func(t *testing.T) {
			config, upstream := fixture(t)
			if mode == "client-uid" {
				config.RunnerUID++
			}
			if mode == "upstream-uid" {
				config.UpstreamUID++
			}
			session, err := Start(config)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			accepted := make(chan *net.UnixConn, 1)
			go func() { conn, _ := upstream.AcceptUnix(); accepted <- conn }()
			t.Cleanup(func() {
				upstream.Close()
				select {
				case conn := <-accepted:
					if conn != nil {
						conn.Close()
					}
				default:
				}
			})
			client, err := net.Dial("unix", session.Path)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			client.SetDeadline(time.Now().Add(2 * time.Second))
			if mode == "close" {
				var backend *net.UnixConn
				select {
				case backend = <-accepted:
				case <-time.After(2 * time.Second):
					t.Fatal("upstream not connected")
				}
				defer backend.Close()
				if err := session.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := client.Read(make([]byte, 1)); err == nil {
				t.Fatal("connection survived rejection/close")
			}
		})
	}
}
func TestProxyRootGuards(t *testing.T) {
	config, _ := fixture(t)
	original := config.Directory
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(original, link); err != nil {
		t.Fatal(err)
	}
	config.Directory = link
	if _, err := Start(config); err == nil {
		t.Fatal("symlink proxy root")
	}
	config.Directory = original
	if err := os.Chmod(original, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(config); err == nil {
		t.Fatal("writable proxy root")
	}
}
