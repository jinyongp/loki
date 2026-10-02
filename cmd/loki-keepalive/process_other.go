//go:build !windows

package main

func keepalive(string) int { return 1 }
