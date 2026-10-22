package secretoutput

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"
)

// Deliberately non-streaming and independent of the production automaton: at
// every byte offset, choose the longest literal match, or copy one byte.
func reference(data []byte, patterns [][]byte) []byte {
	var result []byte
	for offset := 0; offset < len(data); {
		length := 0
		for _, pattern := range patterns {
			if len(pattern) > length && bytes.HasPrefix(data[offset:], pattern) {
				length = len(pattern)
			}
		}
		if length == 0 {
			result = append(result, data[offset])
			offset++
		} else {
			result = append(result, []byte("[REDACTED]")...)
			offset += length
		}
	}
	return result
}
func checkChunks(t *testing.T, matcher *Matcher, data, want []byte, size int) {
	t.Helper()
	var output bytes.Buffer
	writer := NewWriter(&output, matcher)
	for begin := 0; begin < len(data); begin += size {
		end := min(len(data), begin+size)
		if n, err := writer.Write(data[begin:end]); err != nil || n != end-begin {
			t.Fatal("stream write", n, err)
		}
		if len(writer.pending) > max(1, matcher.longest) || writer.position-writer.consumed >= uint64(max(1, matcher.longest)) {
			t.Fatal("unbounded pending bytes")
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), want) {
		t.Fatalf("wrong masking chunk=%d data=%q got=%q want=%q", size, data, output.Bytes(), want)
	}
}

func TestResearchFixturesAndIndependentRandomReference(t *testing.T) {
	fixtures := []struct {
		data     []byte
		patterns [][]byte
	}{
		{[]byte("xxTOKENyyTOKEN"), [][]byte{[]byte("TOKEN")}},
		{[]byte("abcabcab"), [][]byte{[]byte("ab"), []byte("abc"), []byte("bc"), {}}},
		{[]byte("앞토큰-가나다뒤"), [][]byte{[]byte("토큰-가나다")}},
		{[]byte("first\nsecond!first\nsecond"), [][]byte{[]byte("first\nsecond")}},
		{[]byte("banana"), [][]byte{[]byte("a")}},
	}
	// The first five are the supplied Python model's concrete fixtures. These
	// additional cases use Go's independent deterministic generator, not Python's
	// PRNG; fixture counts do not imply identical generated inputs.
	rng := rand.New(rand.NewSource(20261019))
	alphabet := []byte("abcXYZ01")
	for range 1000 {
		patterns := make([][]byte, 1+rng.Intn(8))
		for i := range patterns {
			patterns[i] = make([]byte, 1+rng.Intn(12))
			for j := range patterns[i] {
				patterns[i][j] = alphabet[rng.Intn(len(alphabet))]
			}
		}
		var data []byte
		choices := append(append([][]byte{}, patterns...), []byte("-"), []byte("noise"), []byte("\n"))
		for count := 1 + rng.Intn(15); count > 0; count-- {
			data = append(data, choices[rng.Intn(len(choices))]...)
		}
		fixtures = append(fixtures, struct {
			data     []byte
			patterns [][]byte
		}{data, patterns})
	}
	for _, fixture := range fixtures {
		matcher, err := New(fixture.patterns)
		if err != nil {
			t.Fatal(err)
		}
		want := reference(fixture.data, fixture.patterns)
		for _, size := range []int{1, 2, 3, 5, 8, 13, 64} {
			checkChunks(t, matcher, fixture.data, want, size)
		}
		matcher.Destroy()
	}
}

func TestEveryPartitionAndSuffixOverlap(t *testing.T) {
	for _, fixture := range []struct {
		data     string
		patterns []string
	}{
		{"ABCDEX", []string{"ABC", "BCDE", "DE", "longer-placeholder"}},
		{"abcabcab", []string{"ab", "abc", "bc"}},
		{"aaaaaab", []string{"a", "aa", "aaa", "aab"}},
		{string([]byte{0xff, 0xc3, 0xa9, 0x80}), []string{"é", string([]byte{0xff})}},
	} {
		patterns := make([][]byte, len(fixture.patterns))
		for i, p := range fixture.patterns {
			patterns[i] = []byte(p)
		}
		matcher, err := New(patterns)
		if err != nil {
			t.Fatal(err)
		}
		data := []byte(fixture.data)
		want := reference(data, patterns)
		for partition := 0; partition < 1<<max(0, len(data)-1); partition++ {
			var output bytes.Buffer
			writer := NewWriter(&output, matcher)
			begin := 0
			for i := 1; i <= len(data); i++ {
				if i == len(data) || partition&(1<<(i-1)) != 0 {
					if _, err := writer.Write(data[begin:i]); err != nil {
						t.Fatal(err)
					}
					begin = i
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(output.Bytes(), want) {
				t.Fatalf("partition=%d data=%q got=%q want=%q", partition, data, output.Bytes(), want)
			}
		}
		matcher.Destroy()
	}
}

func TestJSONBodiesAndIndependentStreams(t *testing.T) {
	matcher, err := New([][]byte{[]byte("q\"\n<>&"), []byte("TOKEN")})
	if err != nil {
		t.Fatal(err)
	}
	defer matcher.Destroy()
	data := []byte("q\"\n<>&|q\\\"\\n<>&|q\\\"\\n\\u003c\\u003e\\u0026")
	for size := 1; size <= len(data); size++ {
		checkChunks(t, matcher, data, []byte("[REDACTED]|[REDACTED]|[REDACTED]"), size)
	}
	var stdout, stderr bytes.Buffer
	out, diagnostic := NewWriter(&stdout, matcher), NewWriter(&stderr, matcher)
	out.Write([]byte("TO"))
	diagnostic.Write([]byte("KEN"))
	out.Close()
	diagnostic.Close()
	if stdout.String() != "TO" || stderr.String() != "KEN" {
		t.Fatal("stream states were merged")
	}
	if fmt.Sprintf("%+v", *matcher) != "[REDACTED]" || fmt.Sprintf("%+v", *out) != "[REDACTED]" {
		t.Fatal("pattern diagnostic leak")
	}
}

type failedSink struct{ writes int }

func (s *failedSink) Write([]byte) (int, error) { s.writes++; return 0, io.ErrClosedPipe }
func TestOutputFailureNeverFallsBack(t *testing.T) {
	matcher, _ := New([][]byte{[]byte("TOKEN")})
	defer matcher.Destroy()
	sink := &failedSink{}
	writer := NewWriter(sink, matcher)
	if _, err := writer.Write([]byte("TOKEN")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("raw-must-not-flow")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	if sink.writes != 1 {
		t.Fatal("retried output after failure")
	}
	if err := writer.Close(); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}
func TestPatternLimitsRejectBeforeUse(t *testing.T) {
	for _, patterns := range [][][]byte{
		{bytes.Repeat([]byte{'x'}, MaxPatternBytes+1)},
		func() [][]byte {
			ps := make([][]byte, MaxPatterns+1)
			for i := range ps {
				ps[i] = []byte(fmt.Sprintf("distinct-%04d", i))
			}
			return ps
		}(),
		func() [][]byte {
			ps := make([][]byte, MaxOutputsPerState+1)
			for i := range ps {
				ps[i] = bytes.Repeat([]byte{'a'}, i+1)
			}
			return ps
		}(),
	} {
		if _, err := New(patterns); !errors.Is(err, ErrLimit) {
			t.Fatal("limit did not reject", err)
		}
	}
	matcher, err := New([][]byte{nil, {}, []byte("x"), []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	if !matcher.HasShortPatterns() {
		t.Fatal("short secret silently excluded")
	}
	checkChunks(t, matcher, []byte("xxx"), []byte(strings.Repeat("[REDACTED]", 3)), 1)
	matcher.Destroy()
}

type blockedSink struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockedSink) Write(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return len(p), nil
}
func TestAbortKeepsPatternsForBlockedWriteWithoutWaiting(t *testing.T) {
	matcher, _ := New([][]byte{[]byte("TOKEN")})
	sink := &blockedSink{entered: make(chan struct{}), release: make(chan struct{})}
	writer := NewWriter(sink, matcher)
	done := make(chan error, 1)
	go func() { _, err := writer.Write([]byte("TOKEN")); done <- err }()
	<-sink.entered
	aborted := make(chan struct{})
	go func() { writer.Abort(); matcher.Destroy(); close(aborted) }()
	select {
	case <-aborted:
	case <-time.After(time.Second):
		t.Fatal("abort waited for reader")
	}
	// The automaton is retained by the active write until its sink returns.
	if len(matcher.states) == 0 {
		t.Fatal("active output lost patterns")
	}
	close(sink.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("blocked writer did not stop")
	}
	if len(matcher.states) != 0 {
		t.Fatal("patterns not erased after final callback")
	}
	if _, err := writer.Write([]byte("TOKEN")); !errors.Is(err, ErrClosed) {
		t.Fatal("abort became raw fallback", err)
	}
}

func FuzzChunkedMatchesReference(f *testing.F) {
	f.Add([]byte("abcXYZabc"), []byte{0, 1, 2}, uint8(1))
	f.Add([]byte{0xff, 0x80, 'a', 'a', 'a'}, []byte{0, 0, 0}, uint8(3))
	f.Fuzz(func(t *testing.T, data, seed []byte, chunk uint8) {
		if len(data) > 4096 || len(seed) > 128 {
			return
		}
		alphabet := []byte("abcXYZ01")
		patterns := [][]byte{}
		for i := 0; i < len(seed); {
			n := min(len(seed)-i, 1+int(seed[i]%16))
			p := make([]byte, n)
			for j := range p {
				p[j] = alphabet[int(seed[i+j])%len(alphabet)]
			}
			patterns = append(patterns, p)
			i += n
		}
		matcher, err := New(patterns)
		if err != nil {
			t.Fatal(err)
		}
		defer matcher.Destroy()
		checkChunks(t, matcher, data, reference(data, patterns), 1+int(chunk%64))
	})
}

func TestMaximumPatternAndEOFPrefixStayBounded(t *testing.T) {
	pattern := bytes.Repeat([]byte{'a'}, MaxPatternBytes)
	matcher, err := New([][]byte{pattern})
	if err != nil {
		t.Fatal(err)
	}
	defer matcher.Destroy()
	data := append(append(append([]byte{}, pattern...), pattern...), pattern[:len(pattern)-1]...)
	want := append([]byte("[REDACTED][REDACTED]"), pattern[:len(pattern)-1]...)
	for _, size := range []int{1, 257, 32768} {
		checkChunks(t, matcher, data, want, size)
	}
}
func TestTotalNormalizedPatternBudget(t *testing.T) {
	patterns := make([][]byte, 0, 65)
	for i := 0; i < 64; i++ {
		p := bytes.Repeat([]byte{'a'}, MaxPatternBytes)
		copy(p[len(p)-2:], []byte(fmt.Sprintf("%02d", i)))
		patterns = append(patterns, p)
	}
	patterns = append(patterns, []byte("z"))
	if _, err := New(patterns); !errors.Is(err, ErrLimit) {
		t.Fatal("normalized total budget not enforced", err)
	}
}

func TestUnrelatedProgressIsNotHeldBehindLongestPattern(t *testing.T) {
	matcher, err := New([][]byte{[]byte("fixture-token-with-a-long-prefix")})
	if err != nil {
		t.Fatal(err)
	}
	defer matcher.Destroy()
	var output bytes.Buffer
	writer := NewWriter(&output, matcher)
	if _, err := writer.Write([]byte("ready\n")); err != nil {
		t.Fatal(err)
	}
	if output.String() != "ready\n" {
		t.Fatal("unrelated startup marker withheld until EOF")
	}
	if _, err := writer.Write([]byte("fixture-")); err != nil {
		t.Fatal(err)
	}
	if output.String() != "ready\n" {
		t.Fatal("possible secret prefix escaped")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}
