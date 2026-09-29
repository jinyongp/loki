package main

import (
	"bytes"
	"io"
	"os"
	"sync"

	"golang.org/x/term"
)

type crlfWriter struct {
	mu         sync.Mutex
	writer     io.Writer
	previousCR bool
}

func consoleOutputWriter(file *os.File) io.Writer {
	if file == nil || !term.IsTerminal(int(file.Fd())) {
		return file
	}
	return &crlfWriter{writer: file}
}

func (writer *crlfWriter) Write(raw []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()

	if len(raw) == 0 {
		return 0, nil
	}
	normalized := make([]byte, 0, len(raw)+bytes.Count(raw, []byte{'\n'}))
	previousCR := writer.previousCR
	for _, value := range raw {
		if value == '\n' && !previousCR {
			normalized = append(normalized, '\r')
		}
		normalized = append(normalized, value)
		previousCR = value == '\r'
	}
	writer.previousCR = previousCR

	written, err := writer.writer.Write(normalized)
	if err != nil {
		return 0, err
	}
	if written != len(normalized) {
		return 0, io.ErrShortWrite
	}
	return len(raw), nil
}
