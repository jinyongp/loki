package privateio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestFrameBoundsAndPartialWrites(t *testing.T) {
	var wire bytes.Buffer
	if err := WriteBytes(&wire, []byte("abcd"), 4); err != nil {
		t.Fatal(err)
	}
	body, err := ReadBytes(&wire, 4)
	if err != nil || string(body) != "abcd" {
		t.Fatal(err)
	}
	for _, n := range []uint32{0, 5, 0xffffffff} {
		var h [4]byte
		binary.BigEndian.PutUint32(h[:], n)
		if _, err := ReadBytes(bytes.NewReader(h[:]), 4); !errors.Is(err, ErrLimit) {
			t.Fatal("unbounded allocation", n, err)
		}
	}
	if _, err := ReadBytes(bytes.NewReader([]byte{0, 0, 0, 4, 'a'}), 4); err != io.ErrUnexpectedEOF {
		t.Fatal(err)
	}
	if err := WriteBytes(zeroWriter{}, []byte("x"), 4); err != io.ErrShortWrite {
		t.Fatal(err)
	}
	short := shortWriter{}
	if err := WriteBytes(&short, []byte("abcd"), 4); err != nil || len(short.data) != 8 {
		t.Fatal(err)
	}
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

type shortWriter struct{ data []byte }

func (w *shortWriter) Write(b []byte) (int, error) { w.data = append(w.data, b[0]); return 1, nil }

func TestPrivateControlRejectsAmbiguousJSON(t *testing.T) {
	var value struct {
		Protocol int `json:"protocol"`
		Nested   struct {
			Name string `json:"name"`
		} `json:"nested"`
	}
	for _, body := range []string{`null`, `[]`, `{"protocol":1,"Protocol":2}`, `{"protocol":1,"protocol":2}`, `{"protocol":1,"nested":{"name":"a","name":"b"}}`, `{"protocol":null}`, `{"protocol":1,"unexpected":true}`, `{"protocol":1} {}`, `{"protocol":1,"nested":{"Name":"x"}}`} {
		if Decode([]byte(body), &value) == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	if err := Decode([]byte(`{"protocol":1,"nested":{"name":"x"}}`), &value); err != nil {
		t.Fatal(err)
	}
}
