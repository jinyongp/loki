package management

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSystemProtocolRejectsCallerAuthority(t *testing.T) {
	for _, flag := range []string{"--catalog", "-catalog", "--catalog=/tmp/x", "-archives=/tmp/x", "--key-file", "-private-key-file", "--root=/tmp/x", "--remote-command=x", "--system-socket=/tmp/x"} {
		if _, _, err := ValidateSystemArguments([]string{"tools", "install", "git", flag, "/tmp/x"}); err == nil {
			t.Fatalf("administrator option accepted: %s", flag)
		}
	}
	for _, args := range [][]string{{"install"}, {"upgrade", "--yes"}, {"tools", "configure", "--mode", "full"}, {"_system-host"}, {"status", strings.Repeat("x", 4097)}} {
		if _, _, err := ValidateSystemArguments(args); err == nil {
			t.Fatalf("administrator command accepted: %v", args)
		}
	}
	args, input, err := ValidateSystemArguments([]string{"--json", "--root", "/ignored", "tools", "serve"})
	if err != nil || !input || strings.Join(args, " ") != "--json tools serve" {
		t.Fatalf("duplex normalization: %v %v %v", args, input, err)
	}
	if _, input, err := ValidateSystemArguments([]string{"integrations", "setup", "git", "--key-stdin", "--identity-name", "Example", "--identity-email", "x@example.com"}); err != nil || !input {
		t.Fatalf("protected input: %v %v", input, err)
	}
}

func TestSystemProtocolDuplexKeepsChannelsSeparate(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "child")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat\nprintf 'diagnostic' >&2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(root, "socket"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ServeSystemHost(ctx, listener, script, root, uint32(os.Getuid())) }()
	conn, err := net.DialUnix("unix", nil, listener.Addr().(*net.UnixAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewEncoder(conn).Encode(systemRequest{Args: []string{"integrations", "setup", "git", "--key-stdin"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "protected-input"); err != nil {
		t.Fatal(err)
	}
	conn.CloseWrite()
	channels := map[byte]string{}
	for {
		var header [5]byte
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			t.Fatal(err)
		}
		size := binary.BigEndian.Uint32(header[1:])
		if size > 64<<10 {
			t.Fatal("unbounded frame")
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(conn, data); err != nil {
			t.Fatal(err)
		}
		channels[header[0]] += string(data)
		if header[0] == 3 {
			break
		}
	}
	if channels[1] != "protected-input" || channels[2] != "diagnostic" || channels[3] != "0" {
		t.Fatalf("channels or EOF were lost: %v", channels)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not stop")
	}
}
