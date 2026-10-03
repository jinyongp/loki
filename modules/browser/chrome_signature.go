package browser

import (
	"debug/macho"
	"encoding/binary"
	"fmt"
	"os"
)

// Chrome for Testing 154.0.8037.92 ships unsigned on Intel and with a
// linker-generated ad-hoc code signature on arm64. Archive receipts and owned
// generation integrity bind the whole app; neither input has a resource seal.
func chromeSignatureKind(path, arch string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	header := make([]byte, 32)
	if _, err := file.ReadAt(header, 0); err != nil {
		return fmt.Errorf("truncated Chrome Mach-O header: %w", err)
	}
	le := binary.LittleEndian
	if le.Uint32(header[:4]) != 0xfeedfacf || le.Uint32(header[20:24]) > 1024*1024 || le.Uint32(header[16:20]) > le.Uint32(header[20:24])/8 {
		return fmt.Errorf("invalid Chrome Mach-O header")
	}
	f, err := macho.NewFile(file)
	if err != nil {
		return fmt.Errorf("Chrome Mach-O input: %w", err)
	}
	defer f.Close()
	expected := macho.CpuAmd64
	if arch == "arm64" {
		expected = macho.CpuArm64
	} else if arch != "amd64" {
		return fmt.Errorf("unsupported Chrome signature architecture %q", arch)
	}
	if f.Cpu != expected {
		return fmt.Errorf("Chrome signature architecture mismatch")
	}
	var signature []byte
	for _, load := range f.Loads {
		raw := load.Raw()
		if len(raw) < 8 || f.ByteOrder.Uint32(raw) != 0x1d {
			continue
		}
		if signature != nil || len(raw) != 16 {
			return fmt.Errorf("invalid Chrome signature command")
		}
		size := f.ByteOrder.Uint32(raw[12:16])
		if size < 12 || size > 1024*1024 {
			return fmt.Errorf("invalid Chrome signature size")
		}
		signature = make([]byte, size)
		_, err = file.ReadAt(signature, int64(f.ByteOrder.Uint32(raw[8:12])))
		if err != nil {
			return fmt.Errorf("truncated Chrome signature: %w", err)
		}
	}
	if arch == "amd64" {
		if signature != nil {
			return fmt.Errorf("pinned Intel Chrome must retain its unsigned input")
		}
		return nil
	}
	be := binary.BigEndian
	if len(signature) < 12 || be.Uint32(signature[:4]) != 0xfade0cc0 || int(be.Uint32(signature[4:8])) > len(signature) {
		return fmt.Errorf("invalid Chrome signature container")
	}
	length := int(be.Uint32(signature[4:8]))
	count := int(be.Uint32(signature[8:12]))
	if length < 12 || count > (length-12)/8 {
		return fmt.Errorf("invalid Chrome signature index")
	}
	found := false
	for i := 0; i < count; i++ {
		entry := signature[12+i*8 : 20+i*8]
		if be.Uint32(entry[:4]) != 0 {
			continue
		}
		offset := int(be.Uint32(entry[4:]))
		if found || offset < 12+count*8 || offset > length-28 {
			return fmt.Errorf("invalid Chrome code directory")
		}
		cd := signature[offset:length]
		if be.Uint32(cd[:4]) != 0xfade0c02 || be.Uint32(cd[4:8]) < 28 || int(be.Uint32(cd[4:8])) > len(cd) || be.Uint32(cd[12:16]) != 0x20002 || be.Uint32(cd[24:28]) != 0 {
			return fmt.Errorf("Chrome must retain its pinned linker ad-hoc signature without a resource seal")
		}
		found = true
	}
	if !found {
		return fmt.Errorf("missing Chrome ad-hoc code directory")
	}
	return nil
}
