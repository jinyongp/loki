// Package privateio owns bounded private byte framing. It has no vault,
// process, Docker, public RPC or application authorization authority.
package privateio

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

const MaxControlFrame = 128 << 10
const MaxPayloadFrame = 16 << 20

var ErrInvalid = errors.New("invalid private frame")
var ErrLimit = errors.New("private frame exceeds limit")

func WriteBytes(w io.Writer, body []byte, limit int) error {
	if limit < 1 || len(body) == 0 || len(body) > limit {
		return ErrLimit
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(body)))
	for _, part := range [][]byte{header[:], body} {
		for len(part) > 0 {
			n, err := w.Write(part)
			if n < 0 || n > len(part) {
				return io.ErrShortWrite
			}
			if err != nil {
				return err
			}
			if n == 0 {
				return io.ErrShortWrite
			}
			part = part[n:]
		}
	}
	return nil
}

// ReadBytes refuses untrusted lengths before allocation. The caller owns the
// returned sensitive buffer and must clear it after decoding/forwarding.
func ReadBytes(r io.Reader, limit int) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if limit < 1 || n == 0 || uint64(n) > uint64(limit) {
		return nil, ErrLimit
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		clear(body)
		return nil, err
	}
	return body, nil
}

func WriteControl(w io.Writer, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return ErrInvalid
	}
	defer clear(body)
	return WriteBytes(w, body, MaxControlFrame)
}

func ReadControl(r io.Reader, value any) error {
	body, err := ReadBytes(r, MaxControlFrame)
	if err != nil {
		return err
	}
	defer clear(body)
	return Decode(body, value)
}

// Decode rejects aliases, duplicates, nulls, unknown fields and trailing JSON
// at every depth. Wire object field names are always lowercase; value names are
// string contents rather than map keys. Error messages never echo input bytes.
func Decode(body []byte, target any) error {
	if !utf8.Valid(body) || len(bytes.TrimSpace(body)) == 0 || bytes.TrimSpace(body)[0] != '{' {
		return ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var scan func(int) error
	scan = func(depth int) error {
		if depth > 32 {
			return ErrInvalid
		}
		token, err := decoder.Token()
		if err != nil || token == nil {
			return ErrInvalid
		}
		switch token {
		case json.Delim('{'):
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return ErrInvalid
				}
				name, ok := key.(string)
				if !ok || name == "" || seen[name] || name != strings.ToLower(name) {
					return ErrInvalid
				}
				seen[name] = true
				if err = scan(depth + 1); err != nil {
					return err
				}
			}
			if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
				return ErrInvalid
			}
		case json.Delim('['):
			for decoder.More() {
				if err = scan(depth + 1); err != nil {
					return err
				}
			}
			if closing, err := decoder.Token(); err != nil || closing != json.Delim(']') {
				return ErrInvalid
			}
		case json.Delim('}'), json.Delim(']'):
			return ErrInvalid
		}
		return nil
	}
	if err := scan(0); err != nil {
		return ErrInvalid
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrInvalid
	}
	decoder = json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrInvalid
	}
	return nil
}
