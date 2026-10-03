package browserapp

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOfficialTransportRejectsWrongPeerBeforeInitialization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	data := make(chan int, 1)
	go func() {
		connection, err := listener.AcceptUnix()
		if err != nil {
			data <- -1
			return
		}
		defer connection.Close()
		_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
		buffer := make([]byte, 1024)
		count, _ := connection.Read(buffer)
		data <- count
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	transport := OfficialTransport{Socket: path, ExpectedUID: uint32(os.Getuid()) + 1}
	if _, err := transport.Connect(ctx); err == nil {
		t.Fatal("accepted another service identity")
	}
	select {
	case count := <-data:
		if count != 0 {
			t.Fatalf("sent initialization data to wrong peer: %d", count)
		}
	case <-ctx.Done():
		t.Fatal("identity rejection did not close transport")
	}
}
