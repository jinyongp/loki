package management

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// Readers and scanners can briefly hold Windows handles without delete sharing.
// Retain the old state until replacement succeeds; never unlink it to retry.
func replaceStateFile(source, destination string) error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := os.Rename(source, destination)
		if err == nil || (!errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED)) || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
}
