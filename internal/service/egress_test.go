package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"loki/internal/audit"
	"loki/internal/egress"
)

func TestEgressProxyRoleLifecycle(t *testing.T) {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	policy := egress.Policy{Version: egress.PolicyVersion, Profiles: map[string]egress.Profile{"dependency-install": {AllowedHosts: []string{"github.com"}, AllowedPorts: []int{443}}}}
	log := &audit.Log{Path: filepath.Join(t.TempDir(), "egress.jsonl")}
	go func() {
		done <- RunEgressProxy(ctx, listener, policy, "dependency-install", log, func() error { close(ready); return nil }, func(err error) { t.Error(err) })
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("readiness timeout")
	}
	conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	fmt.Fprint(conn, "CONNECT private.example:443 HTTP/1.1\r\nHost: private.example:443\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || response.StatusCode != 403 {
		t.Fatal(response, err)
	}
	response.Body.Close()
	records, err := log.Read(10)
	if err != nil || len(records["records"].([]json.RawMessage)) != 1 {
		t.Fatalf("audit records = %#v, %v", records, err)
	}
	var record map[string]any
	if err = json.Unmarshal(records["records"].([]json.RawMessage)[0], &record); err != nil {
		t.Fatal(err)
	}
	if record["operation"] != "egress-connect" || record["profile"] != "dependency-install" || record["host"] != "private.example" || record["port"] != float64(443) || record["allowed"] != false || record["reason"] != "not-allowlisted" {
		t.Fatalf("audit record = %#v", record)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown timeout")
	}
}
