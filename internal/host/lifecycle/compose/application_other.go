//go:build !linux

package compose

import "errors"

func applicationRuntimeDirectory(string, string) (string, error) {
	return "", errors.New("application service preparation requires the native Linux host adapter")
}
