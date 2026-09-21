package workspace

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"loki/internal/fault"
	"loki/internal/policy"
)

const MaxImageBytes = 10 * 1024 * 1024

func ImageFormat(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".gif":
		return "gif"
	case ".jpg", ".jpeg":
		return "jpeg"
	case ".png":
		return "png"
	case ".webp":
		return "webp"
	}
	return ""
}
func imageSignature(data []byte, format string) bool {
	switch format {
	case "gif":
		return bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a"))
	case "jpeg":
		return bytes.HasPrefix(data, []byte{255, 216, 255})
	case "png":
		return bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n"))
	case "webp":
		return len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && string(data[8:12]) == "WEBP"
	}
	return false
}

func (f *Files) Image(path string) ([]byte, map[string]any, error) {
	data, _, err := f.read(path, min(f.Config.MaxFileBytes, MaxImageBytes))
	if err != nil {
		return nil, nil, err
	}
	format := ImageFormat(path)
	if format == "" {
		return nil, nil, fault.Error("unsupported image format")
	}
	if !imageSignature(data, format) {
		return nil, nil, fault.Error("image signature does not match its extension")
	}
	relative, _ := policy.Relative(path)
	return data, map[string]any{"path": relative, "mime_type": "image/" + format, "bytes": len(data), "sha256": Digest(data)}, nil
}

func (f *Files) WriteImage(path, encoded, mime string, overwrite bool, expected string) (map[string]any, error) {
	return f.writeImage(path, encoded, mime, overwrite, expected, "write_image")
}
func (f *Files) SaveScreenshot(path, encoded string, overwrite bool, expected string, fullPage bool) (map[string]any, error) {
	if strings.ToLower(filepath.Ext(path)) != ".png" {
		return nil, fault.Error("browser screenshot path must use a .png extension")
	}
	result, err := f.writeImage(path, encoded, "image/png", overwrite, expected, "browser_save_screenshot")
	if err == nil {
		result["full_page"] = fullPage
	}
	return result, err
}
func (f *Files) writeImage(path, encoded, mime string, overwrite bool, expected, operation string) (map[string]any, error) {
	format := strings.TrimPrefix(strings.ToLower(mime), "image/")
	switch format {
	case "gif", "jpeg", "png", "webp":
	default:
		return nil, fault.Error("unsupported image MIME type")
	}
	if _, err := f.Policy.Resolve(path, false); err != nil {
		return nil, err
	}
	actual := ImageFormat(path)
	if actual == "" {
		return nil, fault.Error("image path must use a supported extension")
	}
	if format != actual {
		return nil, fault.Error("image MIME type does not match path extension")
	}
	encoded = strings.TrimPrefix(encoded, "data:"+mime+";base64,")
	if len(encoded) > ((MaxImageBytes+2)/3)*4 {
		return nil, fault.Error("encoded image exceeds write limit")
	}
	if strings.ContainsAny(encoded, "\r\n\t ") {
		return nil, fault.Error("invalid base64 image data")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fault.Error("invalid base64 image data")
	}
	if len(data) > MaxImageBytes {
		return nil, fault.Error("image exceeds write limit")
	}
	if !imageSignature(data, format) {
		return nil, fault.Error("image signature does not match its extension")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.reconcileBatchesLocked(); err != nil {
		return nil, err
	}
	current, info, err := f.read(path, 64<<20)
	var revision any
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if exists {
		if !overwrite {
			return nil, os.ErrExist
		}
		if Digest(current) != expected {
			return nil, fault.Error("existing image changed or was not read before overwrite")
		}
		revision, err = f.capture(path, operation, current, info.Mode())
		if err != nil {
			return nil, err
		}
	}
	if err = f.Policy.AtomicWrite(path, data, 0600, exists); err != nil {
		return nil, err
	}
	relative, _ := policy.Relative(path)
	return map[string]any{"path": relative, "bytes": len(data), "mime_type": strings.ToLower(mime), "sha256": Digest(data), "previous_revision": revision}, nil
}
