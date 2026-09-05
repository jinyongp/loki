// Package redact removes private values before process output crosses an RPC
// boundary. Offsets belong to the original byte stream, not the rendered text.
package redact

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
)

const marker = "[REDACTED]"

// Filter is immutable and safe to share between concurrent snapshots. Values
// remain private; neither JSON encoding nor diagnostic formatting exposes them.
type Filter struct {
	values  [][]byte
	longest int
}

func New(values []string) (*Filter, error) {
	if len(values) > 512 {
		return nil, errors.New("redaction value count exceeds limit")
	}
	f := &Filter{}
	seen := map[string]bool{}
	total := 0
	for _, value := range values {
		if len(value) > 1048576 {
			return nil, errors.New("redaction value exceeds limit")
		}
		if value == "" || seen[value] {
			continue
		}
		total += len(value)
		if total > 128*1048576 {
			return nil, errors.New("redaction data exceeds limit")
		}
		seen[value] = true
		f.values = append(f.values, []byte(value))
		f.longest = max(f.longest, len(value))
	}
	slices.SortFunc(f.values, func(a, b []byte) int { return len(b) - len(a) })
	return f, nil
}

func (f Filter) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[private redaction filter]")
}
func (f Filter) MarshalJSON() ([]byte, error) {
	return nil, errors.New("private redaction filter cannot be serialized")
}

// Context is the extra private prefix that a tail buffer must retain so a
// secret beginning before available_from can still be recognized in a page.
func (f *Filter) Context() int {
	if f == nil {
		return 0
	}
	return max(0, f.longest-1)
}

// Slice renders a public window using its surrounding private stream context.
// Complete, overlapping and adjacent matches form one redacted run. A suffix
// that could become a secret on the next write is masked immediately; readers
// never receive speculative secret prefixes. No hidden context is returned.
func (f *Filter) Slice(data []byte, start, end int) []byte {
	start = min(max(0, start), len(data))
	end = min(max(start, end), len(data))
	if f == nil || len(f.values) == 0 || start == end {
		return bytes.Clone(data[start:end])
	}
	masked := make([]bool, end-start)
	mark := func(from, to int) {
		for i := max(start, from); i < min(end, to); i++ {
			masked[i-start] = true
		}
	}
	for _, value := range f.values {
		search := max(0, start-len(value)+1)
		last := min(len(data), end+len(value)-1)
		markedTo := 0
		unfinished := matches(value, data[search:last], func(from, to int) {
			mark(search+max(markedTo, from), search+to)
			markedTo = to
		})
		if last == len(data) {
			mark(len(data)-unfinished, len(data))
		}
	}
	out := make([]byte, 0, end-start)
	for i := 0; i < len(masked); {
		if !masked[i] {
			out = append(out, data[start+i])
			i++
			continue
		}
		out = append(out, marker...)
		for i < len(masked) && masked[i] {
			i++
		}
	}
	return out
}

// KMP visits all matches and returns the unfinished prefix length in linear
// time, including repetitive secrets. Scratch space is bounded by one secret,
// rather than retaining a large automaton for every profile/action instance.
func matches(pattern, text []byte, visit func(int, int)) int {
	if len(pattern) == 0 || len(text) == 0 {
		return 0
	}
	failure := make([]int32, min(len(pattern), len(text)))
	for i, matched := 1, 0; i < len(failure); i++ {
		for matched > 0 && pattern[i] != pattern[matched] {
			matched = int(failure[matched-1])
		}
		if pattern[i] == pattern[matched] {
			matched++
		}
		failure[i] = int32(matched)
	}
	matched := 0
	for i, b := range text {
		for matched > 0 && b != pattern[matched] {
			matched = int(failure[matched-1])
		}
		if b == pattern[matched] {
			matched++
		}
		if matched == len(pattern) {
			if visit != nil {
				visit(i+1-matched, i+1)
			}
			matched = int(failure[matched-1])
		}
	}
	return matched
}
