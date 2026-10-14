package main

import (
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"
)

// Keep the last diagnostics in memory so long commands cannot grow buffered output
// without bound. Subprocess stdout and stderr can write concurrently.
type diagnosticTail struct {
	mu        sync.Mutex
	tail      []byte
	truncated bool
}

func (d *diagnosticTail) Write(p []byte) (int, error) {
	const limit = 256 << 10
	d.mu.Lock()
	defer d.mu.Unlock()
	n := len(p)
	if len(d.tail)+n > limit {
		d.truncated = true
		if n >= limit {
			d.tail = append(d.tail[:0], p[n-limit:]...)
			return n, nil
		}
		d.tail = append(d.tail[:0], d.tail[len(d.tail)+n-limit:]...)
	}
	d.tail = append(d.tail, p...)
	return n, nil
}

func (d *diagnosticTail) String() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	text := ansi.Strip(string(d.tail))
	if d.truncated {
		text = "Earlier progress omitted; latest diagnostics follow.\n" + text
	}
	return strings.TrimSpace(text)
}
