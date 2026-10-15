package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"loki/internal/progress"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

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
	err = setupWorking(t.Context(), terminal, "Fixture action", "Fixture host", func(log io.Writer) error {
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

// Exercise real input ownership while a persistent setup screen is active.
func setupSessionInputFixture(t *testing.T, input, terminal *os.File, kind string) {
	t.Helper()
	ctx, session := setupSessionContext(t.Context(), input, terminal)
	defer session.print(terminal)
	err := setupWorking(ctx, terminal, "Input fixture", "Fixture host", func(log io.Writer) error {
		if kind == "session-cancel" {
			fmt.Fprintln(log, "Waiting for cancellation")
			<-ctx.Done()
			return ctx.Err()
		}
		stop := progress.StartHeartbeat(ctx, progress.NewLineReporter(log), progress.HeartbeatOptions{Every: time.Millisecond, Message: "Waiting for administrator input"})
		defer stop()
		password, err := browserAdministratorPassword(ctx, browserEnvironmentReport{AdministratorRequired: true, Missing: []string{"fixture-package"}}, input, log)
		defer clear(password)
		if err != nil {
			return err
		}
		if string(password) != "fixture-secret-password" {
			return errors.New("password input was consumed by the menu")
		}
		fmt.Fprintln(log, "Authentication fixture complete")
		return nil
	})
	if kind == "session-cancel" {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("working cancellation was lost: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := showSetupResult(ctx, "Password input — accepted", "Protected input reached the action.", "", input, terminal); err != nil {
		t.Fatal(err)
	}
}
