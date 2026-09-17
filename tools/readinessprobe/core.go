package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/config"
	"loki/internal/process"
)

type r3Observation struct {
	LegacyUnknownKeysAcceptedAndIgnored bool `json:"legacy_unknown_keys_accepted_and_ignored"`
	MisspelledKeyAccepted               bool `json:"misspelled_key_accepted"`
	FractionalIntegerAccepted           bool `json:"fractional_integer_accepted"`
}

type r7Observation struct {
	TimeoutReported                   bool `json:"timeout_reported"`
	DetachedDescendantSurvivedTimeout bool `json:"detached_descendant_survived_timeout"`
}

func observeConfig() (r3Observation, error) {
	empty, err := config.Parse(nil)
	if err != nil {
		return r3Observation{}, err
	}
	legacy, legacyErr := config.Parse([]byte("max_processes=1\n[executables]\nnode='/unused'\n[checks]\ntest=['false']\n"))
	_, typoErr := config.Parse([]byte("max_output_byte=8192\n"))
	_, fractionalErr := config.Parse([]byte("max_output_bytes=8192.9\n"))
	return r3Observation{
		LegacyUnknownKeysAcceptedAndIgnored: legacyErr == nil && reflect.DeepEqual(empty, legacy),
		MisspelledKeyAccepted:               typoErr == nil,
		FractionalIntegerAccepted:           fractionalErr == nil,
	}, nil
}

func observeProcessCleanup() (r7Observation, error) {
	ctx := context.Background()
	root, err := os.MkdirTemp("", "loki-readiness-process-")
	if err != nil {
		return r7Observation{}, err
	}
	defer os.RemoveAll(root)
	pidFile := filepath.Join(root, "child.pid")
	program := filepath.Join(root, "detach.py")
	source := "import os,sys,time\n" +
		"pid=os.fork()\n" +
		"if pid==0:\n" +
		" os.setsid()\n" +
		" for fd in (0,1,2):\n" +
		"  n=os.open('/dev/null',os.O_RDWR);os.dup2(n,fd);os.close(n)\n" +
		" open(sys.argv[1],'w').write(str(os.getpid()))\n" +
		" time.sleep(10)\n" +
		" os._exit(0)\n" +
		"time.sleep(10)\n"
	if err = os.WriteFile(program, []byte(source), 0600); err != nil {
		return r7Observation{}, err
	}
	result, runErr := process.Run(ctx, process.Spec{
		Argv:      []string{"/usr/bin/python3", program, pidFile},
		CWD:       root,
		Env:       []string{"PATH=/usr/bin:/bin", "HOME=" + root, "LANG=C.UTF-8", "LC_ALL=C.UTF-8"},
		Timeout:   300 * time.Millisecond,
		MaxOutput: 1024,
	})
	if runErr != nil {
		return r7Observation{}, runErr
	}

	var childPID int
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(pidFile)
		if readErr == nil {
			childPID, err = strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				return r7Observation{}, err
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		return r7Observation{}, fmt.Errorf("detached child did not publish pid")
	}
	alive := unix.Kill(childPID, 0) == nil
	if alive {
		_ = unix.Kill(childPID, unix.SIGKILL)
		for range 100 {
			if unix.Kill(childPID, 0) != nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	return r7Observation{TimeoutReported: result.TimedOut, DetachedDescendantSurvivedTimeout: alive}, nil
}
