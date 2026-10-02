package progress

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const LinePrefix = "[loki] "

type State string

const (
	StateStarted   State = "started"
	StateCompleted State = "completed"
	StateInfo      State = "info"
)

type Event struct {
	Operation string
	Phase     string
	State     State
	Message   string
	Level     Level
}

// Internal phases are detailed by default. Callers explicitly identify the
// few operation summaries and long-wait notices useful in normal CLI output.
type Level string

const (
	LevelDetail    Level = ""
	LevelSummary   Level = "summary"
	LevelHeartbeat Level = "heartbeat"
)

type Reporter interface {
	Report(Event)
}

type ReporterFunc func(Event)

func (fn ReporterFunc) Report(event Event) {
	if fn != nil {
		fn(event)
	}
}

func Emit(reporter Reporter, event Event) {
	if reporter == nil {
		return
	}
	reporter.Report(event)
}

type lineReporter struct {
	mu           sync.Mutex
	w            io.Writer
	activity     chan struct{}
	verbose      bool
	waitReported bool
}

func NewLineReporter(w io.Writer) Reporter {
	if w == nil {
		return nil
	}
	return &lineReporter{w: w, activity: make(chan struct{}, 1), verbose: Verbose(w)}
}

func (reporter *lineReporter) Report(event Event) {
	message := strings.TrimSpace(event.Message)
	if message == "" {
		return
	}
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	if !reporter.verbose {
		switch event.Level {
		case LevelSummary:
		case LevelHeartbeat:
			if reporter.waitReported {
				return
			}
			reporter.waitReported = true
		default:
			return
		}
	}
	_, _ = fmt.Fprintln(reporter.w, LinePrefix+message)
	select {
	case reporter.activity <- struct{}{}:
	default:
	}
}

func (reporter *lineReporter) Verbose() bool { return reporter.verbose }

func (reporter *lineReporter) Activity() <-chan struct{} {
	return reporter.activity
}

func IsProgressLine(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), LinePrefix)
}

func NonProgressText(raw string) string {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if IsProgressLine(line) {
			continue
		}
		if strings.TrimSpace(line) != "" {
			kept = append(kept, strings.TrimSpace(line))
		}
	}
	return strings.Join(kept, "\n")
}

type ReaderOptions struct {
	Operation  string
	Phase      string
	Label      string
	TotalBytes int64
	EveryBytes int64
}

type reportingReader struct {
	reader       io.Reader
	reporter     Reporter
	options      ReaderOptions
	readBytes    int64
	nextReportAt int64
	lastReported int64
}

func NewReader(reader io.Reader, reporter Reporter, options ReaderOptions) io.Reader {
	if reader == nil || reporter == nil {
		return reader
	}
	if options.EveryBytes <= 0 {
		options.EveryBytes = 32 << 20
	}
	return &reportingReader{
		reader:       reader,
		reporter:     reporter,
		options:      options,
		nextReportAt: options.EveryBytes,
	}
}

func (reader *reportingReader) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	reader.readBytes += int64(count)
	if reader.readBytes >= reader.nextReportAt {
		reader.report()
		for reader.nextReportAt <= reader.readBytes {
			reader.nextReportAt += reader.options.EveryBytes
		}
	}
	if err == io.EOF && reader.readBytes != reader.lastReported && reader.readBytes > 0 {
		reader.report()
	}
	return count, err
}

func (reader *reportingReader) report() {
	label := strings.TrimSpace(reader.options.Label)
	if label == "" {
		label = "Downloaded"
	}
	message := fmt.Sprintf("%s: %s", label, formatBytes(reader.readBytes))
	if reader.options.TotalBytes > 0 {
		message += " / " + formatBytes(reader.options.TotalBytes)
	}
	Emit(reader.reporter, Event{
		Operation: reader.options.Operation,
		Phase:     reader.options.Phase,
		State:     StateInfo,
		Message:   message,
	})
	reader.lastReported = reader.readBytes
}

func formatBytes(value int64) string {
	const (
		kib = int64(1 << 10)
		mib = int64(1 << 20)
	)
	switch {
	case value < kib:
		return fmt.Sprintf("%d B", value)
	case value < mib:
		return fmt.Sprintf("%.1f KiB", float64(value)/float64(kib))
	default:
		return fmt.Sprintf("%.1f MiB", float64(value)/float64(mib))
	}
}

type HeartbeatOptions struct {
	Operation string
	Phase     string
	Message   string
	Every     time.Duration
}

func StartHeartbeat(
	ctx context.Context,
	reporter Reporter,
	options HeartbeatOptions,
) func() {
	if reporter == nil {
		return func() {}
	}
	interval := options.Every
	if interval <= 0 {
		interval = 30 * time.Second
	}
	started := time.Now()
	done := make(chan struct{})
	exited := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(exited)
		timer := time.NewTimer(interval)
		defer timer.Stop()
		var activity <-chan struct{}
		if active, ok := reporter.(interface{ Activity() <-chan struct{} }); ok {
			activity = active.Activity()
		}
		reset := func() {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(interval)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-activity:
				reset()
			case <-timer.C:
				message := strings.TrimSpace(options.Message)
				if message == "" {
					message = "Still working"
				}
				Emit(reporter, Event{
					Operation: options.Operation,
					Phase:     options.Phase,
					State:     StateInfo,
					Message:   fmt.Sprintf("%s (%s elapsed)...", message, time.Since(started).Round(time.Second)),
					Level:     LevelHeartbeat,
				})
				reset()
			}
		}
	}()
	return func() {
		once.Do(func() { close(done) })
		<-exited
	}
}
