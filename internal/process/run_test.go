package process

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBoundedBuffer(t *testing.T) {
	b := NewBuffer(100)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() { b.Write([]byte(strings.Repeat("x", 30))) })
	}
	wg.Wait()
	data, truncated := b.Snapshot()
	if len(data) != 100 || !truncated {
		t.Fatal("buffer exceeded bounds")
	}
	data[0] = 'y'
	again, _ := b.Snapshot()
	if again[0] != 'x' {
		t.Fatal("snapshot exposed buffer")
	}
}

func TestRunExitTimeoutAndTruncation(t *testing.T) {
	r, err := Run(context.Background(), Spec{Argv: []string{"/usr/bin/printf", "hello"}, MaxOutput: 3})
	if err != nil || r.Output != "hel" || !r.Truncated || r.ExitCode != 0 {
		t.Fatalf("bounded run: %v %v", r, err)
	}
	r, err = Run(context.Background(), Spec{Argv: []string{"/usr/bin/false"}})
	if err != nil || r.ExitCode != 1 {
		t.Fatalf("exit: %v %v", r, err)
	}
	r, err = Run(context.Background(), Spec{Argv: []string{"/usr/bin/sleep", "5"}, Timeout: 20 * time.Millisecond})
	if err != nil || !r.TimedOut || r.ExitCode != 124 {
		t.Fatalf("timeout: %v %v", r, err)
	}
	if _, err = Run(context.Background(), Spec{Argv: []string{"/not-an-executable"}}); err == nil {
		t.Fatal("missing executable reported success")
	}
}

func TestUTF8ReplacementAndByteLimits(t *testing.T) {
	for _, tc := range []struct {
		raw             []byte
		limit           int
		sourceTruncated bool
		want            string
		clipped         bool
	}{
		{[]byte("\xffabcde"), 6, true, "\ufffdabc", true},
		{[]byte("한글")[:4], 4, true, "한", true},
		{[]byte("\xe2\x82\xfftail"), 32, false, "\ufffd\ufffdtail", false},
		{[]byte("\xe2\x82"), 32, false, "\ufffd", false},
		{[]byte("\xff\xff"), 32, false, "\ufffd\ufffd", false},
		{[]byte("\xed\xa0\x80"), 32, false, "\ufffd\ufffd\ufffd", false},
		{[]byte("\ufffdok"), 5, false, "\ufffdok", false},
		{[]byte("\xffabc"), 4, false, "\ufffda", true},
	} {
		got, clipped := boundedText(tc.raw, tc.limit, tc.sourceTruncated)
		if got != tc.want || clipped != tc.clipped || len(got) > tc.limit {
			t.Errorf("decode %x: got %q/%v want %q/%v", tc.raw, got, clipped, tc.want, tc.clipped)
		}
	}
	r, err := Run(t.Context(), Spec{Argv: []string{"/usr/bin/printf", "%b", "\\xffabcde-extra"}, MaxOutput: 6})
	if err != nil || r.Output != "\ufffdabc" || !r.Truncated {
		t.Fatalf("run decoded output %q: %v", r.Output, err)
	}
}
