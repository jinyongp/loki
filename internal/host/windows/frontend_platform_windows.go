//go:build windows

package windows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type WindowsFrontendPlatform struct {
	Runner NativeRunner
}

func NewWindowsFrontendPlatform() WindowsFrontendPlatform {
	return WindowsFrontendPlatform{Runner: ExecNativeRunner{}}
}

func (platform WindowsFrontendPlatform) Lstat(path string) (StatePath, error) {
	return OSStateFilesystem{}.Lstat(path)
}

func (platform WindowsFrontendPlatform) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (platform WindowsFrontendPlatform) FileDigest(path string) (string, int64, error) {
	info, err := platform.Lstat(path)
	if err != nil {
		return "", 0, err
	}
	if !info.Exists || !info.Regular || info.Reparse {
		return "", 0, errors.New("Windows frontend file is not a regular non-reparse file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	length, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), length, nil
}

func (platform WindowsFrontendPlatform) EnsurePrivateDirectory(ctx context.Context, target string) error {
	if err := validateExistingDirectoryPrefixes(target); err != nil {
		return err
	}
	info, err := platform.Lstat(target)
	if err != nil {
		return err
	}
	if !info.Exists {
		if err = os.MkdirAll(target, 0o700); err != nil {
			return err
		}
		if err = validateExistingDirectoryPrefixes(target); err != nil {
			return err
		}
		info, err = platform.Lstat(target)
		if err != nil {
			return err
		}
	}
	if !info.Directory || info.Reparse {
		return errors.New("Windows Loki program path is not a real directory")
	}
	current, err := user.Current()
	if err != nil {
		return fmt.Errorf("resolve current Windows user: %w", err)
	}
	if platform.Runner == nil {
		return errors.New("Windows native runner is unavailable")
	}
	return (WindowsFreshPlatform{Runner: platform.Runner}).protectDirectory(ctx, target, current.Username)
}

func validateExistingDirectoryPrefixes(target string) error {
	clean := filepath.Clean(target)
	volume := filepath.VolumeName(clean)
	if volume == "" {
		return errors.New("Windows Loki program path has no local volume")
	}
	remainder := strings.TrimLeft(strings.TrimPrefix(clean, volume), "\\/")
	current := volume + string(os.PathSeparator)
	for _, component := range strings.FieldsFunc(remainder, func(value rune) bool {
		return value == '\\' || value == '/'
	}) {
		current = filepath.Join(current, component)
		info, err := OSStateFilesystem{}.Lstat(current)
		if err != nil {
			return err
		}
		if !info.Exists {
			return nil
		}
		if !info.Directory || info.Reparse {
			return fmt.Errorf("Windows Loki program path crosses unsafe component %q", current)
		}
	}
	return nil
}

func (platform WindowsFrontendPlatform) PublishExecutable(ctx context.Context, source, target string) error {
	if _, _, err := platform.FileDigest(source); err != nil {
		return err
	}
	if err := validateExistingDirectoryPrefixes(filepath.Dir(target)); err != nil {
		return err
	}
	temp, err := copySyncedTemp(source, filepath.Dir(target), ".loki-frontend-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp)
	if err = platform.protectFile(ctx, temp); err != nil {
		return err
	}
	return replaceFile(temp, target)
}

func (platform WindowsFrontendPlatform) WriteProtectedAtomic(ctx context.Context, target string, raw []byte) error {
	if err := validateExistingDirectoryPrefixes(filepath.Dir(target)); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(target), ".loki-state-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err = temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err = temp.Write(raw); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	if err = platform.protectFile(ctx, tempName); err != nil {
		return err
	}
	return replaceFile(tempName, target)
}

func (platform WindowsFrontendPlatform) UserPath(context.Context) (string, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer key.Close()
	value, _, err := key.GetStringValue("Path")
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil
	}
	return value, err
}

func (platform WindowsFrontendPlatform) SetUserPath(_ context.Context, value string) error {
	if strings.ContainsRune(value, 0) {
		return errors.New("current-user PATH contains NUL")
	}
	key, _, err := registry.CreateKey(
		registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE|registry.SET_VALUE,
	)
	if err != nil {
		return err
	}
	_, valueType, readErr := key.GetStringValue("Path")
	if readErr != nil && !errors.Is(readErr, registry.ErrNotExist) {
		key.Close()
		return readErr
	}
	switch valueType {
	case registry.EXPAND_SZ:
		err = key.SetExpandStringValue("Path", value)
	default:
		err = key.SetStringValue("Path", value)
	}
	if err != nil {
		key.Close()
		return err
	}
	if err = key.Close(); err != nil {
		return err
	}
	return broadcastEnvironmentChange()
}

func (platform WindowsFrontendPlatform) Sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (platform WindowsFrontendPlatform) protectFile(ctx context.Context, target string) error {
	current, err := user.Current()
	if err != nil {
		return fmt.Errorf("resolve current Windows user: %w", err)
	}
	if platform.Runner == nil {
		return errors.New("Windows native runner is unavailable")
	}
	return (WindowsFreshPlatform{Runner: platform.Runner}).protectFile(ctx, target, current.Username)
}

func copySyncedTemp(source, directory, pattern string) (string, error) {
	input, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer input.Close()
	temp, err := os.CreateTemp(directory, pattern)
	if err != nil {
		return "", err
	}
	name := temp.Name()
	cleanup := func(cause error) (string, error) {
		temp.Close()
		_ = os.Remove(name)
		return "", cause
	}
	if err = temp.Chmod(0o700); err != nil {
		return cleanup(err)
	}
	if _, err = io.Copy(temp, input); err != nil {
		return cleanup(err)
	}
	if err = temp.Sync(); err != nil {
		return cleanup(err)
	}
	if err = temp.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func replaceFile(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func broadcastEnvironmentChange() error {
	environment, err := windows.UTF16PtrFromString("Environment")
	if err != nil {
		return err
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	sendMessageTimeout := user32.NewProc("SendMessageTimeoutW")
	var result uintptr
	ret, _, callErr := sendMessageTimeout.Call(
		uintptr(0xffff),
		uintptr(0x001a),
		0,
		uintptr(unsafe.Pointer(environment)),
		uintptr(0x0002),
		uintptr(5000),
		uintptr(unsafe.Pointer(&result)),
	)
	if ret == 0 {
		if callErr != nil && callErr != windows.ERROR_SUCCESS {
			return fmt.Errorf("broadcast Windows environment change: %w", callErr)
		}
		return errors.New("broadcast Windows environment change timed out")
	}
	return nil
}
