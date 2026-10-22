package secretoutput

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// Writer implements leftmost-longest masking with at most longest-pattern minus
// one bytes withheld between writes. Streams have independent rings; patterns
// and generated replacement text are never re-read as application output.
type Writer struct {
	aborted     *atomic.Bool
	released    bool
	mu          *sync.Mutex
	matcher     *Matcher
	destination io.Writer
	pending     []byte
	matches     []uint32
	position    uint64
	consumed    uint64
	at          uint32
	output      []byte
	closed      bool
	failed      error
}

func (Writer) Format(s fmt.State, _ rune) { io.WriteString(s, "[REDACTED]") }
func (Writer) MarshalJSON() ([]byte, error) {
	return nil, errors.New("redaction stream cannot be serialized")
}
func (Writer) MarshalText() ([]byte, error) {
	return nil, errors.New("redaction stream cannot be serialized")
}
func NewWriter(destination io.Writer, matcher *Matcher) *Writer {
	retained := matcher.retain()
	capacity := 1
	if retained && matcher.HasPatterns() {
		capacity = matcher.longest
	}
	writer := &Writer{mu: &sync.Mutex{}, aborted: &atomic.Bool{}, released: !retained, matcher: matcher, destination: destination, pending: make([]byte, capacity), matches: make([]uint32, capacity), output: make([]byte, 0, outputChunk)}
	if !retained {
		writer.failed = ErrClosed
	}
	return writer
}
func (w *Writer) flush() error {
	if len(w.output) == 0 {
		return nil
	}
	if w.aborted.Load() {
		return ErrClosed
	}
	n, err := w.destination.Write(w.output)
	if err == nil && n != len(w.output) {
		err = io.ErrShortWrite
	}
	clear(w.output)
	w.output = w.output[:0]
	return err
}
func (w *Writer) emit(p []byte) error {
	for len(p) > 0 {
		n := min(len(p), outputChunk-len(w.output))
		w.output = append(w.output, p[:n]...)
		p = p[n:]
		if len(w.output) == outputChunk {
			if err := w.flush(); err != nil {
				return err
			}
		}
	}
	return nil
}
func (w *Writer) commit() error {
	capacity := uint64(len(w.pending))
	slot := w.consumed % capacity
	length := uint64(w.matches[slot])
	if length > 0 {
		if err := w.emit([]byte(Replacement)); err != nil {
			return err
		}
	} else {
		if err := w.emit(w.pending[slot : slot+1]); err != nil {
			return err
		}
		length = 1
	}
	for range length {
		slot := w.consumed % capacity
		w.pending[slot] = 0
		w.matches[slot] = 0
		w.consumed++
	}
	return nil
}
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	defer func() {
		if w.aborted.Load() {
			w.closed = true
			w.destroy()
		}
	}()
	if w.closed || w.aborted.Load() {
		return 0, ErrClosed
	}
	if w.failed != nil {
		return 0, w.failed
	}
	if w.destination == nil {
		w.failed = io.ErrClosedPipe
		return 0, w.failed
	}
	if !w.matcher.HasPatterns() {
		n, err := w.destination.Write(p)
		if err == nil && n != len(p) {
			err = io.ErrShortWrite
		}
		w.failed = err
		return n, err
	}
	capacity := uint64(len(w.pending))
	for i, value := range p {
		w.pending[w.position%capacity] = value
		w.position++
		w.at = w.matcher.next(w.at, value)
		for node := w.at; node != 0; node = w.matcher.states[node].output {
			length := uint64(w.matcher.states[node].terminal)
			if length == 0 {
				continue
			}
			start := w.position - length
			if start >= w.consumed && length > uint64(w.matches[start%capacity]) {
				w.matches[start%capacity] = uint32(length)
			}
		}
		// Only an active trie prefix can still extend into a future match.
		// Discard suffix state overlapping an already chosen earlier match.
		// Everything before the earliest live prefix is safe to commit now.
		for w.consumed < w.position {
			for w.at != 0 && uint64(w.matcher.states[w.at].depth) > w.position-w.consumed {
				w.at = w.matcher.states[w.at].fail
			}
			st := w.matcher.states[w.at]
			earliest := w.position - uint64(st.depth)
			completeLeaf := w.consumed == earliest && st.count == 0 && w.matches[w.consumed%capacity] > 0
			if w.consumed == earliest && !completeLeaf {
				break
			}
			if err := w.commit(); err != nil {
				w.failed = err
				return i + 1, err
			}
		}
	}
	if err := w.flush(); err != nil {
		w.failed = err
		return len(p), err
	}
	return len(p), nil
}

// Close is an actual EOF, not an idle timer. Abort drops pending bytes instead
// when a stream fails or no reliable EOF was obtained. Neither closes the sink.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.failed
	}
	w.closed = true
	defer w.destroy()
	if w.failed != nil {
		return w.failed
	}
	for w.consumed < w.position {
		if err := w.commit(); err != nil {
			w.failed = err
			return err
		}
	}
	w.failed = w.flush()
	return w.failed
}
func (w *Writer) destroy() {
	if !w.released {
		w.released = true
		w.matcher.release()
	}
	clear(w.pending)
	clear(w.matches)
	clear(w.output)
	w.pending = nil
	w.matches = nil
	w.output = nil
}

// Abort does not wait behind a blocked destination. The active writer retains
// patterns and erases pending data when it returns; no plaintext goroutine spool
// or forced flush is introduced to bypass backpressure.
func (w *Writer) Abort() {
	w.aborted.Store(true)
	if w.mu.TryLock() {
		defer w.mu.Unlock()
		w.closed = true
		w.destroy()
	}
}
