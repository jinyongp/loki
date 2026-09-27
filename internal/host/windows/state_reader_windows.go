//go:build windows

package windows

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

type OSStateFilesystem struct{}

func (OSStateFilesystem) Lstat(path string) (StatePath, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return StatePath{}, nil
	}
	if err != nil {
		return StatePath{}, err
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return StatePath{}, err
	}
	attributes, err := windows.GetFileAttributes(pointer)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return StatePath{}, nil
	}
	if err != nil {
		return StatePath{}, err
	}
	return StatePath{
		Exists:    true,
		Directory: info.IsDir(),
		Regular:   info.Mode().IsRegular(),
		Reparse:   attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0,
	}, nil
}

func (OSStateFilesystem) ReadDir(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

func (OSStateFilesystem) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
