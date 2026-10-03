// Package githubapp validates administrator-owned GitHub App credentials.
package githubapp

import (
	"context"
	"crypto/rsa"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
	"loki/internal/fault"
	"loki/modules/github/keys"
)

const MaxPrivateKeyBytes = keys.MaxPrivateKeyBytes

func LoadPrivateKeyFile(ctx context.Context, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fault.Error("GitHub App credential is unavailable")
	}
	select {
	case <-ctx.Done():
		return "", fault.Error("GitHub App credential is unavailable")
	default:
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", fault.Error("GitHub App credential is unavailable")
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() <= 0 || info.Size() > MaxPrivateKeyBytes {
		return "", fault.Error("GitHub App credential is unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxPrivateKeyBytes+1))
	if err != nil || len(data) > MaxPrivateKeyBytes {
		clear(data)
		return "", fault.Error("GitHub App credential is unavailable")
	}
	defer clear(data)
	value := string(data)
	if ValidatePrivateKey(value) != nil {
		return "", fault.Error("GitHub App credential is unavailable")
	}
	return value, nil
}

func ValidatePrivateKey(value string) error { return keys.ValidatePrivateKey(value) }

func parsePrivateKey(value string) (*rsa.PrivateKey, error) { return keys.ParsePrivateKey(value) }
