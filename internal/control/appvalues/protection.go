package appvalues

import (
	"bytes"
	"encoding/binary"
	"errors"
	"unicode/utf8"
)

const MaxProtectionInput = 4 + 2*65536

// ProtectionInput is carried only in the private byte frame after AdminRequest.
// It cannot be used as a JSON or command-line credential representation.
func ProtectionInput(current, next []byte) ([]byte, error) {
	if len(current) > 65536 || len(next) == 0 || len(next) > 65536 {
		return nil, errors.New("invalid protection input")
	}
	body := make([]byte, 4+len(current)+len(next))
	binary.BigEndian.PutUint32(body, uint32(len(current)))
	copy(body[4:], current)
	copy(body[4+len(current):], next)
	if _, _, err := ProtectionPassphrases(body); err != nil {
		clear(body)
		return nil, err
	}
	return body, nil
}

func ProtectionPassphrases(body []byte) (current, next []byte, err error) {
	invalid := errors.New("invalid protection input")
	if len(body) < 5 || len(body) > MaxProtectionInput {
		return nil, nil, invalid
	}
	n := int(binary.BigEndian.Uint32(body))
	if n > 65536 || n > len(body)-5 || len(body)-4-n > 65536 {
		return nil, nil, invalid
	}
	current, next = body[4:4+n], body[4+n:]
	normalize := func(b []byte) []byte {
		if bytes.HasSuffix(b, []byte{'\n'}) {
			return bytes.TrimSuffix(b[:len(b)-1], []byte{'\r'})
		}
		return b
	}
	valid := func(b []byte) bool { return len(b) <= 65536 && utf8.Valid(b) && !bytes.ContainsAny(b, "\x00\r\n") }
	current, next = normalize(current), normalize(next)
	if !valid(current) || !valid(next) || len(next) == 0 {
		return nil, nil, invalid
	}
	return current, next, nil
}
