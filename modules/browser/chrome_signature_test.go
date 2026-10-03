package browser

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestChromeSignatureKind(t *testing.T) {
	fixture := func(arm, signed bool, flags, special uint32) []byte {
		b := make([]byte, 32)
		le, be := binary.LittleEndian, binary.BigEndian
		le.PutUint32(b, 0xfeedfacf)
		cpu := uint32(0x1000007)
		if arm {
			cpu = 0x100000c
		}
		le.PutUint32(b[4:], cpu)
		le.PutUint32(b[12:], 2)
		if !signed {
			return b
		}
		le.PutUint32(b[16:], 1)
		le.PutUint32(b[20:], 16)
		b = append(b, make([]byte, 64)...)
		le.PutUint32(b[32:], 0x1d)
		le.PutUint32(b[36:], 16)
		le.PutUint32(b[40:], 48)
		le.PutUint32(b[44:], 48)
		be.PutUint32(b[48:], 0xfade0cc0)
		be.PutUint32(b[52:], 48)
		be.PutUint32(b[56:], 1)
		be.PutUint32(b[64:], 20)
		be.PutUint32(b[68:], 0xfade0c02)
		be.PutUint32(b[72:], 28)
		be.PutUint32(b[80:], flags)
		be.PutUint32(b[92:], special)
		return b
	}
	for _, tt := range []struct {
		name, arch string
		data       []byte
		valid      bool
	}{
		{"unsigned Intel", "amd64", fixture(false, false, 0, 0), true},
		{"linker ad-hoc arm64", "arm64", fixture(true, true, 0x20002, 0), true},
		{"unsigned arm64", "arm64", fixture(true, false, 0, 0), false},
		{"signed Intel", "amd64", fixture(false, true, 0x20002, 0), false},
		{"replaced signature", "arm64", fixture(true, true, 2, 0), false},
		{"resource seal", "arm64", fixture(true, true, 0x20002, 3), false},
		{"wrong architecture", "arm64", fixture(false, false, 0, 0), false},
		{"truncated signature", "arm64", fixture(true, true, 0x20002, 0)[:90], false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "Chrome")
			if err := os.WriteFile(path, tt.data, 0600); err != nil {
				t.Fatal(err)
			}
			err := chromeSignatureKind(path, tt.arch)
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v: %v", tt.valid, err)
			}
		})
	}
}
