//go:build !windows

package main

func keepalive(string) int                          { return 1 }
func restoreConnections(string, string, string) int { return 1 }
