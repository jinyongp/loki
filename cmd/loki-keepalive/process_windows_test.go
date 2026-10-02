//go:build windows

package main

import (
	"debug/pe"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

type keepaliveProbe struct {
	PID     int
	Console uintptr
}

func init() {
	mode := os.Getenv("LOKI_KEEPALIVE_TEST_MODE")
	if mode == "caller" {
		command := exec.Command(os.Getenv("LOKI_KEEPALIVE_TEST_EXE"), "--distribution", "loki-test")
		command.Env = append(os.Environ(), "LOKI_KEEPALIVE_TEST_MODE=wsl")
		if command.Start() != nil {
			os.Exit(4)
		}
		raw, _ := json.Marshal(keepaliveProbe{PID: command.Process.Pid})
		if os.WriteFile(os.Getenv("LOKI_KEEPALIVE_CALLER_PROBE"), raw, 0o600) != nil {
			os.Exit(4)
		}
		_ = command.Process.Release()
		os.Exit(0)
	}
	if mode != "wsl" {
		return
	}
	window, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
	raw, _ := json.Marshal(keepaliveProbe{PID: os.Getpid(), Console: window})
	if os.WriteFile(os.Getenv("LOKI_KEEPALIVE_WSL_PROBE"), raw, 0o600) != nil {
		os.Exit(4)
	}
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(os.Getenv("LOKI_KEEPALIVE_STOP")); err == nil {
			os.Exit(7)
		}
		time.Sleep(100 * time.Millisecond)
	}
	os.Exit(4)
}

func TestCompanionHasNoConsoleAndOutlivesCaller(t *testing.T) {
	root := t.TempDir()
	companion := filepath.Join(root, "loki-keepalive.exe")
	if prebuilt := os.Getenv("LOKI_KEEPALIVE_TEST_COMPANION"); prebuilt != "" {
		raw, err := os.ReadFile(prebuilt)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(companion, raw, 0o700); err != nil {
			t.Fatal(err)
		}
	} else {
		build := exec.CommandContext(t.Context(), "go", "build", "-buildvcs=false", "-ldflags", "-H=windowsgui", "-o", companion, ".")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build companion: %v: %s", err, output)
		}
	}
	file, err := pe.Open(companion)
	if err != nil {
		t.Fatal(err)
	}
	if header, ok := file.OptionalHeader.(*pe.OptionalHeader64); !ok || header.Subsystem != pe.IMAGE_SUBSYSTEM_WINDOWS_GUI {
		t.Fatal("companion can allocate a console")
	}
	file.Close()
	if err = os.Mkdir(filepath.Join(root, "System32"), 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "System32", "wsl.exe"), raw, 0o700); err != nil {
		t.Fatal(err)
	}
	callerProbe, childProbe, stop := filepath.Join(root, "caller.json"), filepath.Join(root, "wsl.json"), filepath.Join(root, "stop")
	t.Cleanup(func() { _ = os.WriteFile(stop, nil, 0o600) })
	caller := exec.CommandContext(t.Context(), executable)
	caller.Env = append(os.Environ(), "SystemRoot="+root, "LOKI_KEEPALIVE_TEST_MODE=caller", "LOKI_KEEPALIVE_TEST_EXE="+companion, "LOKI_KEEPALIVE_CALLER_PROBE="+callerProbe, "LOKI_KEEPALIVE_WSL_PROBE="+childProbe, "LOKI_KEEPALIVE_STOP="+stop)
	if output, err := caller.CombinedOutput(); err != nil {
		t.Fatalf("caller: %v: %s", err, output)
	}
	readProbe := func(path string) keepaliveProbe {
		t.Helper()
		for i := 0; i < 500; i++ {
			data, err := os.ReadFile(path)
			var probe keepaliveProbe
			if err == nil && json.Unmarshal(data, &probe) == nil {
				return probe
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("background process did not publish its probe")
		return keepaliveProbe{}
	}
	parent, child := readProbe(callerProbe), readProbe(childProbe)
	if child.Console != 0 {
		t.Fatal("WSL child acquired a console")
	}
	open := func(pid int) syscall.Handle {
		t.Helper()
		handle, err := syscall.OpenProcess(0x00100000|0x1000, false, uint32(pid)) // SYNCHRONIZE | PROCESS_QUERY_LIMITED_INFORMATION
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { syscall.CloseHandle(handle) })
		status, err := syscall.WaitForSingleObject(handle, 0)
		if err != nil || status != 258 {
			t.Fatalf("background process stopped with caller: %d: %v", status, err)
		}
		return handle
	}
	parentHandle := open(parent.PID)
	open(child.PID)
	if err = os.WriteFile(stop, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if status, err := syscall.WaitForSingleObject(parentHandle, 5000); err != nil || status != 0 {
		t.Fatalf("companion did not exit after WSL: %d: %v", status, err)
	}
	var exit uint32
	if err = syscall.GetExitCodeProcess(parentHandle, &exit); err != nil || exit != 7 {
		t.Fatalf("WSL exit code was lost: %d: %v", exit, err)
	}
}
