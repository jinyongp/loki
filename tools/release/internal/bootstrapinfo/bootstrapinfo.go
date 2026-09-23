package bootstrapinfo

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const maxOutputBytes = 64 << 10

type Info struct {
	ReleaseTag            string `json:"release_tag"`
	ReleaseManifestSHA256 string `json:"release_manifest_sha256"`
	HostBinarySHA256      string `json:"host_binary_sha256"`
}

func Inspect(ctx context.Context, binary string) (Info, error) {
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	binary = strings.TrimSpace(binary)
	if binary == "" || !filepath.IsAbs(binary) || filepath.Clean(binary) != binary ||
		binary == string(filepath.Separator) || strings.ContainsRune(binary, 0) {
		return Info{}, errors.New("bootstrap binary must be a clean absolute non-root path")
	}
	info, err := os.Lstat(binary)
	if err != nil {
		return Info{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0111 == 0 {
		return Info{}, errors.New("bootstrap binary must be an executable regular file")
	}

	command := exec.CommandContext(ctx, binary, "--bootstrap-info")
	var stdout, stderr bytes.Buffer
	command.Stdout = &boundedBuffer{buffer: &stdout, maximum: maxOutputBytes}
	command.Stderr = &boundedBuffer{buffer: &stderr, maximum: maxOutputBytes}
	if err = command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			return Info{}, fmt.Errorf("inspect bootstrap release binding: %w", err)
		}
		return Info{}, fmt.Errorf("inspect bootstrap release binding: %w: %s", err, message)
	}
	var result Info
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&result); err != nil {
		return Info{}, errors.New("bootstrap info output is invalid JSON")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Info{}, errors.New("bootstrap info output contains trailing JSON")
	}
	if err = result.Validate(); err != nil {
		return Info{}, err
	}
	return result, nil
}

func (i Info) Validate() error {
	if strings.TrimSpace(i.ReleaseTag) != i.ReleaseTag || !strings.HasPrefix(i.ReleaseTag, "v") || len(i.ReleaseTag) < 2 {
		return errors.New("bootstrap release tag is invalid")
	}
	for name, value := range map[string]string{
		"release-manifest": i.ReleaseManifestSHA256,
		"host-binary":      i.HostBinarySHA256,
	} {
		if len(value) != 64 || strings.ToLower(value) != value {
			return fmt.Errorf("bootstrap %s digest is invalid", name)
		}
		raw, err := hex.DecodeString(value)
		if err != nil || len(raw) != 32 {
			return fmt.Errorf("bootstrap %s digest is invalid", name)
		}
	}
	return nil
}

type boundedBuffer struct {
	buffer  *bytes.Buffer
	maximum int
}

func (w *boundedBuffer) Write(p []byte) (int, error) {
	if w.buffer.Len()+len(p) > w.maximum {
		remaining := w.maximum - w.buffer.Len()
		if remaining > 0 {
			_, _ = w.buffer.Write(p[:remaining])
		}
		return len(p), errors.New("bootstrap info output exceeds size policy")
	}
	return w.buffer.Write(p)
}
