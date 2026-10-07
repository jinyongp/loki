//go:build !windows

package main

func protectTestCredentialFile(string) error { return nil }
