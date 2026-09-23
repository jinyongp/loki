package releases

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"path"
	"strings"
)

const maxTargetBytes = int64(1 << 30)

type TargetDescriptor struct {
	Path   string `json:"path"`
	Length int64  `json:"length"`
	SHA256 string `json:"sha256"`
}

func (d TargetDescriptor) VerifyBytes(raw []byte) error {
	if d.Length <= 0 || d.Length > maxTargetBytes || int64(len(raw)) != d.Length {
		return errors.New("target length does not match release metadata")
	}
	expected, err := hex.DecodeString(d.SHA256)
	if err != nil || len(expected) != sha256.Size {
		return errors.New("target SHA-256 identity is invalid")
	}
	actual := sha256.Sum256(raw)
	if subtle.ConstantTimeCompare(actual[:], expected) != 1 {
		return errors.New("target SHA-256 does not match release metadata")
	}
	return nil
}

func namespacedTargetPath(namespace, relative string) (string, error) {
	if namespace != "releases" && namespace != "toolchains" {
		return "", errors.New("unsupported release target namespace")
	}
	if relative == "" || relative != strings.TrimSpace(relative) || strings.ContainsAny(relative, "\x00\\") ||
		strings.HasPrefix(relative, "/") || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, "../") || path.Clean(relative) != relative {
		return "", errors.New("release target path is invalid")
	}
	return namespace + "/" + relative, nil
}
