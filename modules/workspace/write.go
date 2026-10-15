package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"loki/internal/fault"
	"loki/internal/policy"
)

// writeIfUnchanged checks again after the temporary file and revision are ready.
// Arbitrary external writers can still race the final check and rename; callers
// must arrange exclusive ownership when they require a conditional atomic write.
func (f *Files) writeIfUnchanged(path string, data []byte, mode os.FileMode, expected string) error {
	conflict := func() error {
		return fault.New(fault.CodeConflict, "file changed before publication; edit was not applied", false,
			"stop other writers, read the file again and use its current sha256")
	}
	err := f.Policy.AtomicWriteChecked(path, data, mode, func(current *os.File) error {
		hash := sha256.New()
		size, err := io.Copy(hash, io.LimitReader(current, (64<<20)+1))
		if err != nil {
			return err
		}
		if size > 64<<20 || hex.EncodeToString(hash.Sum(nil)) != expected {
			return conflict()
		}
		return nil
	})
	if errors.Is(err, policy.ErrWriteTargetChanged) {
		return conflict()
	}
	return err
}
