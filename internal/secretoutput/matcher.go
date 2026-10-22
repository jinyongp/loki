// Package secretoutput protects known execution secrets in byte streams. It does not
// infer secrets, decode arbitrary encodings, or isolate a malicious application.
package secretoutput

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"unicode/utf8"
)

var ErrLimit = errors.New("redaction_pattern_limit")
var ErrClosed = errors.New("redaction_stream_closed")

const (
	MaxPatterns          = 4096
	MaxPatternBytes      = 64 * 1024
	MaxTotalPatternBytes = 4 * 1024 * 1024
	MaxStates            = 262144
	MaxOutputsPerState   = 64
	outputChunk          = 32 * 1024
	Replacement          = "[REDACTED]"
)

type edge struct {
	value byte
	next  uint32
}
type state struct {
	depth    uint32
	first    uint32
	count    uint16
	fail     uint32
	output   uint32
	terminal uint32
}
type buildState struct {
	depth    uint32
	edges    map[byte]uint32
	fail     uint32
	output   uint32
	terminal uint32
	outputs  int
}

type matcherLifetime struct {
	mu      sync.Mutex
	users   int
	closing bool
}

// Matcher is immutable and may be shared by separate stdout/stderr Writers.
// Destroy after both streams end. Its automaton is sensitive execution state.
type Matcher struct {
	lifetime *matcherLifetime
	states   []state
	edges    []edge
	root     [256]uint32
	longest  int
	short    bool
}

func (Matcher) Format(s fmt.State, _ rune) { io.WriteString(s, "[REDACTED]") }
func (Matcher) MarshalJSON() ([]byte, error) {
	return nil, errors.New("redaction patterns cannot be serialized")
}
func (Matcher) MarshalText() ([]byte, error) {
	return nil, errors.New("redaction patterns cannot be serialized")
}

// New uses literal byte patterns plus JSON string bodies emitted by Go's
// encoding/json (HTML escaping enabled and disabled). Empty values create no
// pattern. Limits reject the execution; no pattern is silently discarded.
func New(patterns [][]byte) (*Matcher, error) {
	unique := make(map[string]struct{})
	total := 0
	add := func(pattern []byte) error {
		if len(pattern) == 0 {
			return nil
		}
		if len(pattern) > MaxPatternBytes {
			return ErrLimit
		}
		key := string(pattern)
		if _, ok := unique[key]; ok {
			return nil
		}
		if len(unique) >= MaxPatterns || len(pattern) > MaxTotalPatternBytes-total {
			return ErrLimit
		}
		unique[key] = struct{}{}
		total += len(pattern)
		return nil
	}
	for _, pattern := range patterns {
		if err := add(pattern); err != nil {
			return nil, err
		}
		if utf8.Valid(pattern) && len(pattern) > 0 {
			encoded, _ := json.Marshal(string(pattern))
			err := add(encoded[1 : len(encoded)-1])
			clear(encoded)
			if err != nil {
				return nil, err
			}
			// The only difference in the non-HTML form is these three generated
			// escapes; preserve literal backslash sequences from the original value.
			var buf sensitiveBuffer
			encoder := json.NewEncoder(&buf)
			encoder.SetEscapeHTML(false)
			if encoder.Encode(string(pattern)) != nil {
				return nil, ErrLimit
			}
			err = add(buf.bytes[1 : len(buf.bytes)-2])
			clear(buf.bytes)
			if err != nil {
				return nil, err
			}
		}
	}
	matcher := &Matcher{lifetime: &matcherLifetime{}}
	nodes := []buildState{{edges: map[byte]uint32{}}}
	for pattern := range unique {
		if len(pattern) > matcher.longest {
			matcher.longest = len(pattern)
		}
		if len(pattern) < 4 {
			matcher.short = true
		}
		at := uint32(0)
		for i := range len(pattern) {
			next, ok := nodes[at].edges[pattern[i]]
			if !ok {
				if len(nodes) >= MaxStates {
					return nil, ErrLimit
				}
				next = uint32(len(nodes))
				nodes = append(nodes, buildState{depth: nodes[at].depth + 1, edges: map[byte]uint32{}})
				nodes[at].edges[pattern[i]] = next
			}
			at = next
		}
		nodes[at].terminal = uint32(len(pattern))
	}
	// Breadth first failure/output links. Terminal suffix count bounds worst-case
	// per-byte work before a process starts, including repetitive prefix sets.
	queue := make([]uint32, 0, len(nodes))
	for _, child := range nodes[0].edges {
		queue = append(queue, child)
	}
	for head := 0; head < len(queue); head++ {
		at := queue[head]
		failure := nodes[at].fail
		if nodes[failure].terminal > 0 {
			nodes[at].output = failure
		} else {
			nodes[at].output = nodes[failure].output
		}
		nodes[at].outputs = nodes[failure].outputs
		if nodes[at].terminal > 0 {
			nodes[at].outputs++
		}
		if nodes[at].outputs > MaxOutputsPerState {
			return nil, ErrLimit
		}
		for value, child := range nodes[at].edges {
			fallback := failure
			for {
				if next, ok := nodes[fallback].edges[value]; ok {
					nodes[child].fail = next
					break
				}
				if fallback == 0 {
					break
				}
				fallback = nodes[fallback].fail
			}
			queue = append(queue, child)
		}
	}
	matcher.states = make([]state, len(nodes))
	matcher.edges = make([]edge, 0, len(nodes)-1)
	for index, node := range nodes {
		st := state{depth: node.depth, first: uint32(len(matcher.edges)), count: uint16(len(node.edges)), fail: node.fail, output: node.output, terminal: node.terminal}
		for value, next := range node.edges {
			matcher.edges = append(matcher.edges, edge{value, next})
		}
		sort.Slice(matcher.edges[st.first:], func(i, j int) bool {
			return matcher.edges[int(st.first)+i].value < matcher.edges[int(st.first)+j].value
		})
		matcher.states[index] = st
	}
	for value, child := range nodes[0].edges {
		matcher.root[value] = child
	}
	return matcher, nil
}

type sensitiveBuffer struct{ bytes []byte }

func (b *sensitiveBuffer) Write(p []byte) (int, error) {
	b.bytes = append(b.bytes, p...)
	return len(p), nil
}
func (s sensitiveBuffer) Format(f fmt.State, _ rune) { io.WriteString(f, "[REDACTED]") }

func (m *Matcher) next(at uint32, value byte) uint32 {
	for at != 0 {
		st := m.states[at]
		edges := m.edges[int(st.first) : int(st.first)+int(st.count)]
		i := sort.Search(len(edges), func(i int) bool { return edges[i].value >= value })
		if i < len(edges) && edges[i].value == value {
			return edges[i].next
		}
		at = st.fail
	}
	return m.root[value]
}
func (m *Matcher) HasPatterns() bool      { return m != nil && m.longest > 0 }
func (m *Matcher) HasShortPatterns() bool { return m != nil && m.short }

// retain pairs a stream lifetime with its pending writes. Destroy never clears
// an automaton still used by a blocked output callback after process WaitDelay.
func (m *Matcher) retain() bool {
	if m == nil {
		return true
	}
	if m.lifetime == nil {
		return false
	}
	m.lifetime.mu.Lock()
	defer m.lifetime.mu.Unlock()
	if m.lifetime.closing {
		return false
	}
	m.lifetime.users++
	return true
}
func (m *Matcher) release() {
	if m == nil {
		return
	}
	m.lifetime.mu.Lock()
	defer m.lifetime.mu.Unlock()
	m.lifetime.users--
	if m.lifetime.closing && m.lifetime.users == 0 {
		m.erase()
	}
}
func (m *Matcher) erase() {
	clear(m.states)
	clear(m.edges)
	clear(m.root[:])
	m.states = nil
	m.edges = nil
	m.longest = 0
	m.short = false
}
func (m *Matcher) Destroy() {
	if m == nil || m.lifetime == nil {
		return
	}
	m.lifetime.mu.Lock()
	defer m.lifetime.mu.Unlock()
	m.lifetime.closing = true
	if m.lifetime.users == 0 {
		m.erase()
	}
}
