//go:build !windows

package main

import "os"

func setupProgressOutput(*os.File) (func(), error) {
	return func() {}, nil
}
