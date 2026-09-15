// Package githubapp validates administrator-owned GitHub App credentials.
package githubapp

import (
	"bytes"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"

	"loki/internal/fault"
)

const MaxPrivateKeyBytes = 1_048_576

func ValidatePrivateKey(value string) error {
	if len(value) == 0 || len(value) > MaxPrivateKeyBytes {
		return fault.Error("invalid GitHub App private key")
	}
	block, rest := pem.Decode([]byte(value))
	if block == nil || len(bytes.TrimSpace(rest)) != 0 || block.Type != "RSA PRIVATE KEY" && block.Type != "PRIVATE KEY" {
		return fault.Error("invalid GitHub App private key")
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
		return fault.Error("invalid GitHub App private key")
	}
	return nil
}
