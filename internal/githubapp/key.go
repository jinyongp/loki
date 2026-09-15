// Package githubapp validates administrator-owned GitHub App credentials.
package githubapp

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
	"loki/internal/fault"
)

const MaxPrivateKeyBytes = 1_048_576

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

func ValidatePrivateKey(value string) error {
	_, err := parsePrivateKey(value)
	return err
}

func parsePrivateKey(value string) (*rsa.PrivateKey, error) {
	if len(value) == 0 || len(value) > MaxPrivateKeyBytes {
		return nil, fault.Error("invalid GitHub App private key")
	}
	block, rest := pem.Decode([]byte(value))
	if block == nil || len(bytes.TrimSpace(rest)) != 0 || block.Type != "RSA PRIVATE KEY" && block.Type != "PRIVATE KEY" {
		return nil, fault.Error("invalid GitHub App private key")
	}
	var key *rsa.PrivateKey
	var err error
	if block.Type == "RSA PRIVATE KEY" {
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	} else {
		var parsed any
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		if err == nil {
			key, _ = parsed.(*rsa.PrivateKey)
		}
	}
	if err != nil || key == nil || key.N.BitLen() < 2048 || key.Validate() != nil {
		return nil, fault.Error("invalid GitHub App private key")
	}
	return key, nil
}
