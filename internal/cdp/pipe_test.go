package cdp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"
)

func fixture(t *testing.T, onEvent func(Event)) (*Client, *bufio.Reader, *os.File) {
	t.Helper()
	read, serverWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	serverRead, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	c := New(read, write, onEvent)
	t.Cleanup(func() { c.Close(); serverRead.Close(); serverWrite.Close() })
	return c, bufio.NewReader(serverRead), serverWrite
}
func request(t *testing.T, r *bufio.Reader) map[string]any {
	t.Helper()
	data, err := r.ReadBytes(0)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal(data[:len(data)-1], &value); err != nil {
		t.Fatal(err)
	}
	return value
}
func send(t *testing.T, w io.Writer, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(append(data, 0)); err != nil {
		t.Fatal(err)
	}
}
func TestPipeResponsesEventsAndCancellation(t *testing.T) {
	events := make(chan Event, 1)
	c, reader, writer := fixture(t, func(e Event) { events <- e })
	ctx, cancel := context.WithTimeout(t.Context(), time.Second*3)
	defer cancel()
	result := make(chan error, 2)
	go func() {
		var out map[string]any
		err := c.Call(ctx, "tab", "first", map[string]any{"value": "hello"}, &out)
		if err == nil && out["ok"] != true {
			err = io.ErrUnexpectedEOF
		}
		result <- err
	}()
	first := request(t, reader)
	go func() { result <- c.Call(ctx, "tab", "second", nil, nil) }()
	second := request(t, reader)
	send(t, writer, map[string]any{"method": "Runtime.consoleAPICalled", "sessionId": "tab", "params": map[string]any{"type": "log"}})
	send(t, writer, map[string]any{"id": second["id"], "result": map[string]any{}})
	send(t, writer, map[string]any{"id": first["id"], "result": map[string]any{"ok": true}})
	for range 2 {
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
	select {
	case e := <-events:
		if e.Method != "Runtime.consoleAPICalled" || e.SessionID != "tab" {
			t.Fatal(e)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	short, stop := context.WithCancel(ctx)
	go func() { result <- c.Call(short, "", "cancel", nil, nil) }()
	late := request(t, reader)
	stop()
	if err := <-result; err != context.Canceled {
		t.Fatal(err)
	}
	send(t, writer, map[string]any{"id": late["id"], "result": map[string]any{}})
	go func() { result <- c.Call(ctx, "", "error", nil, nil) }()
	failed := request(t, reader)
	send(t, writer, map[string]any{"id": failed["id"], "error": map[string]any{"code": -1, "message": "fixture"}})
	if err := <-result; err == nil {
		t.Fatal("missing protocol error")
	}
}
func TestPipeCloseUnblocksPending(t *testing.T) {
	c, reader, _ := fixture(t, nil)
	done := make(chan error, 1)
	go func() { done <- c.Call(t.Context(), "", "waiting", nil, nil) }()
	_ = request(t, reader)
	c.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed call succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("pending call leaked")
	}
}
