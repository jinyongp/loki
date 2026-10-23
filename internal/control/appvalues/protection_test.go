package appvalues

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestProtectionFrameBoundsAndLineSemantics(t *testing.T) {
	for _, current := range [][]byte{nil, []byte("original\r\n")} {
		body, err := ProtectionInput(current, []byte("target\n"))
		if err != nil {
			t.Fatal(err)
		}
		a, b, err := ProtectionPassphrases(body)
		if err != nil || string(b) != "target" || len(current) != 0 && string(a) != "original" {
			t.Fatal("private credentials changed")
		}
		clear(body)
	}
	for _, pair := range [][2][]byte{{nil, nil}, {nil, []byte("\n")}, {[]byte("a\rb"), []byte("ok")}, {nil, []byte("a\nb")}, {nil, []byte{0xff}}, {nil, []byte{0}}, {nil, bytes.Repeat([]byte{'a'}, 65537)}} {
		if _, err := ProtectionInput(pair[0], pair[1]); err == nil {
			t.Fatal("invalid credential admitted")
		}
	}
	body := make([]byte, 9)
	binary.BigEndian.PutUint32(body, 1<<31)
	if _, _, err := ProtectionPassphrases(body); err == nil {
		t.Fatal("forged split admitted")
	}
	if (AdminRequest{Action: "protection_rewrap"}).InputLimit() != MaxProtectionInput {
		t.Fatal("missing private input bound")
	}
}
