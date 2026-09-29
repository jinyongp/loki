package main

import (
	"bytes"
	"io"
	"os"
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

func TestConsoleOutputPreservesRedirectedBytes(t *testing.T) {
	raw := []byte("{\"ok\":true}\nwarning\r\nUTF-8: 줄바꿈\n")
	t.Run("file", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "output-*")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		writer := consoleOutputWriter(file)
		if writer != file {
			t.Fatal("redirected file unexpectedly received console normalization")
		}
		if n, err := writer.Write(raw); err != nil || n != len(raw) {
			t.Fatalf("write n=%d err=%v", n, err)
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(file)
		if err != nil || !bytes.Equal(got, raw) {
			t.Fatalf("redirected bytes=%q err=%v", got, err)
		}
	})
	t.Run("pipe", func(t *testing.T) {
		reader, file, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		defer file.Close()
		writer := consoleOutputWriter(file)
		if writer != file {
			t.Fatal("redirected pipe unexpectedly received console normalization")
		}
		if n, err := writer.Write(raw); err != nil || n != len(raw) {
			t.Fatalf("write n=%d err=%v", n, err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(reader)
		if err != nil || !bytes.Equal(got, raw) {
			t.Fatalf("redirected bytes=%q err=%v", got, err)
		}
	})
}
