//go:build linux

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestProtectionStdinCancellation(t *testing.T) {
	// An unbuffered pipe makes Write wait until the read has started, so
	// cancellation exercises an active read rather than only its precheck.
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		body, err := readProtectionInput(ctx, r, io.Discard, "", true)
		clear(body)
		done <- err
	}()
	if _, err := w.Write([]byte("synthetic-incomplete-input")); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled credential input succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled credential input waited for writer EOF")
	}
}

func TestProtectionStdinRejectsCharacterDevice(t *testing.T) {
	f, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if body, err := readProtectionInput(context.Background(), f, io.Discard, "", true); !errors.Is(err, unix.ENOTTY) {
		clear(body)
		t.Fatal("character device accepted as private stdin")
	}
}
