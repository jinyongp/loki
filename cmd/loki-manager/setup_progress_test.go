package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

func setupProgressFixture(t *testing.T, terminal *os.File, kind string) {
	t.Helper()
	before, err := term.GetState(int(terminal.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	var failure error
	if kind == "working-failure" {
		failure = errors.New("fixture failed")
	} else if kind == "working-cancel" {
		failure = context.Canceled
	}
	fmt.Fprint(terminal, "Existing terminal history\r\n")
	err = setupWorking(terminal, "Fixture action", "Fixture host", func(log io.Writer) error {
		// More output than the viewport must still stay off normal scrollback.
		fmt.Fprint(log, strings.Repeat("Temporary action log\r\n", 60))
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("action result was lost: %v", err)
	}
	if failure != nil && (!strings.Contains(setupErrorDetails(err), "Temporary action log") || err.Error() != failure.Error()) {
		t.Fatal("failed action lost diagnostics or expanded its short error", err)
	}
	after, err := term.GetState(int(terminal.Fd()))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("progress did not restore console output mode", err)
	}
	fmt.Fprint(terminal, "[RESULT] Fixture action complete\r\n")
}

func assertSetupProgressTranscript(t *testing.T, transcript string) {
	t.Helper()
	enter := strings.Index(transcript, ansi.SetModeAltScreenSaveCursor)
	log := strings.Index(transcript, "Temporary action log")
	exit := strings.Index(transcript, ansi.ResetModeAltScreenSaveCursor)
	result := strings.Index(transcript, "[RESULT] Fixture action complete")
	if enter < 0 || log < enter || exit < log || result < exit {
		t.Fatal("progress was not confined to the alternate screen", transcript)
	}
	if strings.Contains(transcript, ansi.EraseEntireDisplay) {
		t.Fatal("progress erased existing scrollback")
	}
}

func TestSetupDiagnosticsKeepsBoundedFailureTail(t *testing.T) {
	var log diagnosticTail
	log.Write(bytes.Repeat([]byte("x"), 300<<10))
	log.Write([]byte("\nlast package failure\n"))
	if len(log.tail) != 256<<10 || !strings.Contains(log.String(), "Earlier progress omitted") || !strings.HasSuffix(log.String(), "last package failure") {
		t.Fatal("latest failure not retained within bound")
	}
	cause := errors.New("installation failed\ncleanup also failed")
	err := fmt.Errorf("outer action: %w", &setupActionError{err: cause, details: log.String()})
	if !errors.Is(err, cause) || !strings.Contains(setupErrorDetails(err), "last package failure") || !strings.Contains(setupErrorDetails(err), "cleanup also failed") {
		t.Fatal("wrapping lost action details", err)
	}
}

func TestSetupSessionSummarizesOnce(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	session := &setupSession{}
	session.add("Install — completed", "Host: Ubuntu\nBrowser installed.")
	session.add("Install — completed", "Host: Ubuntu\nBrowser installed.")
	session.add("Readiness — ready", "Host: Ubuntu\nBrowser verified.")
	session.print(writer)
	session.print(writer)
	writer.Close()
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Count(text, "[RESULT]") != 1 || strings.Count(text, "Browser installed.") != 1 || !strings.Contains(text, "Browser verified.") {
		t.Fatal("session repeated or lost results", text)
	}
}
