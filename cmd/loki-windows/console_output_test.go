package main

import (
	"bytes"
	"testing"
)

func TestCRLFWriterNormalizesConsoleLineEndingsAcrossWrites(t *testing.T) {
	var output bytes.Buffer
	writer := &crlfWriter{writer: &output}

	for _, chunk := range [][]byte{
		[]byte("first\nsecond\r"),
		[]byte("\nthird\n"),
	} {
		if _, err := writer.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}

	if got, want := output.String(), "first\r\nsecond\r\nthird\r\n"; got != want {
		t.Fatalf("output=%q want=%q", got, want)
	}
}

func TestCRLFWriterPreservesExistingCRLF(t *testing.T) {
	var output bytes.Buffer
	writer := &crlfWriter{writer: &output}

	raw := []byte("first\r\nsecond\r\n")
	if n, err := writer.Write(raw); err != nil || n != len(raw) {
		t.Fatalf("write n=%d err=%v", n, err)
	}
	if got := output.String(); got != string(raw) {
		t.Fatalf("output=%q want=%q", got, raw)
	}
}
