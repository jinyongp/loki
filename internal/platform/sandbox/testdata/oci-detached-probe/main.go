package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const childFlag = "--child"

func main() {
	if len(os.Args) != 2 && len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: oci-detached-probe [--child] HEARTBEAT")
		os.Exit(2)
	}
	if len(os.Args) == 3 {
		if os.Args[1] != childFlag {
			fmt.Fprintln(os.Stderr, "invalid child mode")
			os.Exit(2)
		}
		heartbeat(os.Args[2])
		return
	}

	path := os.Args[1]
	initial, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	command := exec.Command(os.Args[0], childFlag, path)
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := command.Process.Release(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	deadline := time.Now().Add(5 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		raw, readErr := os.ReadFile(path)
		if readErr == nil && string(raw) != string(initial) {
			ready = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !ready {
		fmt.Fprintln(os.Stderr, "detached child heartbeat did not start")
		os.Exit(1)
	}
	fmt.Println("detached-child-ready")
	for {
		time.Sleep(time.Second)
	}
}

func heartbeat(path string) {
	var sequence uint64
	for {
		sequence++
		payload := fmt.Sprintf("%d\n", sequence)
		if err := os.WriteFile(path, []byte(payload), 0600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
