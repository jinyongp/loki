//go:build windows

package windows

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/pe"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

func ensureKeepaliveExecutable(ctx context.Context, expected ExpectedInstallation, stop func() error) error {
	return ensureKeepalivePayload(ctx, expected, stop, keepaliveExecutableBase64)
}

func ensureKeepalivePayload(ctx context.Context, expected ExpectedInstallation, stop func() error, encoded string) error {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) == 0 {
		return errors.New("this frontend has no release-built WSL keepalive companion")
	}
	image, err := pe.NewFile(bytes.NewReader(raw))
	if err != nil {
		return errors.New("WSL keepalive payload is not a Windows executable")
	}
	header, ok := image.OptionalHeader.(*pe.OptionalHeader64)
	compatibleMachine := image.Machine == pe.IMAGE_FILE_MACHINE_AMD64 || (runtime.GOARCH == "arm64" && image.Machine == pe.IMAGE_FILE_MACHINE_ARM64)
	if !ok || header.Subsystem != pe.IMAGE_SUBSYSTEM_WINDOWS_GUI || !compatibleMachine {
		image.Close()
		return errors.New("WSL keepalive payload is not a compatible console-free Windows executable")
	}
	image.Close()
	if !WindowsPathEqual(expected.TaskExecutable, joinWindowsPath(expected.StateDir, "loki-keepalive.exe")) {
		return errors.New("WSL keepalive executable is outside the owned state directory")
	}
	platform := NewWindowsFrontendPlatform()
	if err = validateExistingDirectoryPrefixes(expected.StateDir); err != nil {
		return err
	}
	if err = verifyPrivateACL(expected.StateDir, true); err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	wanted := hex.EncodeToString(digest[:])
	marker := filepath.Join(expected.StateDir, "keepalive.sha256")
	info, err := platform.Lstat(expected.TaskExecutable)
	if err != nil {
		return err
	}
	if info.Exists {
		actual, _, readErr := platform.FileDigest(expected.TaskExecutable)
		if readErr != nil {
			return readErr
		}
		if err = verifyPrivateACL(expected.TaskExecutable, false); err != nil {
			return err
		}
		if actual == wanted {
			return platform.WriteProtectedAtomic(ctx, marker, []byte(wanted))
		}
		// A previous release may have a different companion. Require its private
		// ownership digest before replacing it, and stop its task before writing.
		markInfo, readErr := platform.Lstat(marker)
		if readErr != nil || !markInfo.Exists || !markInfo.Regular || markInfo.Reparse {
			return errors.New("refusing to replace an unowned WSL keepalive executable")
		}
		if err = verifyPrivateACL(marker, false); err != nil {
			return err
		}
		previous, readErr := platform.ReadFile(marker)
		if readErr != nil || strings.TrimSpace(string(previous)) != actual {
			return errors.New("WSL keepalive executable ownership digest changed")
		}
		if stop == nil {
			return errors.New("WSL keepalive executable requires an owned task restart")
		}
		if err = stop(); err != nil {
			return err
		}
	}
	if err = platform.WriteProtectedAtomic(ctx, expected.TaskExecutable, raw); err != nil {
		return fmt.Errorf("publish console-free WSL keepalive: %w", err)
	}
	if err = platform.WriteProtectedAtomic(ctx, marker, []byte(wanted)); err != nil {
		return err
	}
	actual, _, err := platform.FileDigest(expected.TaskExecutable)
	if err != nil {
		return err
	}
	if actual != wanted {
		return errors.New("published WSL keepalive executable digest mismatch")
	}
	return nil
}
