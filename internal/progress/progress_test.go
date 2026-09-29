package progress

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestLineReporterWritesStableProgressLine(t *testing.T) {
	var out bytes.Buffer
	reporter := NewLineReporter(&out)
	Emit(reporter, Event{Operation: "update", Phase: "prepare", State: StateStarted, Message: "Preparing verified update assets..."})
	if got, want := out.String(), "[loki] Preparing verified update assets...\n"; got != want {
		t.Fatalf("output=%q want=%q", got, want)
	}
}

func TestIsProgressLine(t *testing.T) {
	if !IsProgressLine("[loki] Preparing update...") {
		t.Fatal("progress line was not recognized")
	}
	if IsProgressLine("ordinary stderr") {
		t.Fatal("ordinary stderr was recognized as progress")
	}
}

func TestNonProgressTextRemovesProgressLines(t *testing.T) {
	raw := "[loki] Preparing update...\r\nactual warning\r\n[loki] Applying update...\r\n"
	if got, want := NonProgressText(raw), "actual warning"; got != want {
		t.Fatalf("filtered=%q want=%q", got, want)
	}
}

func TestHeartbeatReportsMeasuredElapsedTime(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var out bytes.Buffer
	stop := StartHeartbeat(ctx, NewLineReporter(&out), HeartbeatOptions{
		Operation: "update",
		Phase:     "prefetch",
		Message:   "Still prefetching runtime artifacts",
		Every:     10 * time.Millisecond,
	})
	time.Sleep(25 * time.Millisecond)
	stop()
	got := out.String()
	if !strings.Contains(got, "[loki] Still prefetching runtime artifacts (") ||
		!strings.Contains(got, " elapsed)...") {
		t.Fatalf("heartbeat=%q", got)
	}
}

func TestHeartbeatSuppressesWhileReporterIsActive(t *testing.T) {
	var out bytes.Buffer
	reporter := NewLineReporter(&out)
	stop := StartHeartbeat(t.Context(), reporter, HeartbeatOptions{
		Operation: "update",
		Phase:     "apply",
		Message:   "heartbeat",
		Every:     50 * time.Millisecond,
	})
	time.Sleep(30 * time.Millisecond)
	Emit(reporter, Event{Operation: "update", Phase: "apply", State: StateInfo, Message: "child progress"})
	time.Sleep(35 * time.Millisecond)
	stop()
	if strings.Contains(out.String(), "heartbeat") {
		t.Fatalf("heartbeat was not suppressed by recent activity: %q", out.String())
	}
	if !strings.Contains(out.String(), "child progress") {
		t.Fatalf("child progress missing: %q", out.String())
	}
}

func TestReportingReaderEmitsMeasuredBytes(t *testing.T) {
	raw := bytes.Repeat([]byte("x"), 10)
	var events []Event
	reporter := ReporterFunc(func(event Event) {
		events = append(events, event)
	})
	reader := NewReader(bytes.NewReader(raw), reporter, ReaderOptions{
		Operation:  "install",
		Phase:      "download",
		Label:      "Downloading appliance",
		TotalBytes: int64(len(raw)),
		EveryBytes: 4,
	})
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events=%#v", events)
	}
	if got := events[len(events)-1].Message; got != "Downloading appliance: 10 B / 10 B" {
		t.Fatalf("final progress=%q", got)
	}
}
