package main

import (
	"os"
	"path/filepath"
	"testing"

	"loki/internal/daemon"
	jobsremote "loki/internal/work/jobs/remote"
)

func TestCheckpointJobRunnerUsesConfiguredExecutor(t *testing.T) {
	uid := uint32(os.Getuid())
	runner, err := checkpointJobRunner(mcpLayout{
		ExecutorSocket: filepath.Join(t.TempDir(), "executor.sock"),
		ExecutorUID:    &uid,
	})
	if err != nil {
		t.Fatal(err)
	}
	if runner == nil {
		t.Fatal("checkpoint Job runner is nil")
	}
	if _, err = checkpointJobRunner(mcpLayout{}); err == nil {
		t.Fatal("checkpoint runner accepted missing executor identity")
	}
}

func TestMCPLayoutRequiresExplicitPeers(t *testing.T) {
	uid := uint32(1000)
	executorUID := uint32(1002)
	valid := mcpLayout{
		RuntimeSocket: "/run/runtime.sock", PortGuardSocket: "/run/ports.sock",
		BrowserSocket: "/run/browser.sock", ExecutorSocket: "/run/executor.sock",
		ExecutionContract: "/usr/share/doc/loki/execution-contract.json",
		PackagedSkillRoot: "/opt/loki/share/skills",
		RuntimeUID:        &uid, PortGuardUID: &uid, BrowserUID: &uid, ExecutorUID: &executorUID,
	}
	options, err := valid.options("token")
	if err != nil {
		t.Fatal(err)
	}
	if options.Jobs == nil || options.GitJobs == nil || options.ExecutorSocket != valid.ExecutorSocket {
		t.Fatalf("executor options = %#v", options)
	}
	if _, ok := options.Jobs.(*jobsremote.Executor); !ok {
		t.Fatalf("jobs client type = %T", options.Jobs)
	}
	if _, ok := options.GitJobs.(*jobsremote.Executor); !ok {
		t.Fatalf("Git Job client type = %T", options.GitJobs)
	}

	for _, mutate := range []func(*mcpLayout){
		func(l *mcpLayout) { l.RuntimeUID = nil },
		func(l *mcpLayout) { l.PortGuardUID = nil },
		func(l *mcpLayout) { l.BrowserUID = nil },
		func(l *mcpLayout) { l.RuntimeSocket = "relative" },
		func(l *mcpLayout) { l.RGPath = "relative" },
		func(l *mcpLayout) { l.ExecutionContract = "relative" },
		func(l *mcpLayout) { l.PackagedSkillRoot = "relative" },
		func(l *mcpLayout) { l.ExecutorUID = nil },
		func(l *mcpLayout) { l.ExecutorSocket = "" },
		func(l *mcpLayout) { l.ExecutorSocket = "relative" },
		func(l *mcpLayout) {
			root := uint32(0)
			l.ExecutorUID = &root
		},
	} {
		layout := valid
		mutate(&layout)
		if _, err := layout.options("token"); err == nil {
			t.Fatal("invalid peer/resource configuration accepted")
		}
	}
}

func TestMCPLayoutAllowsBrowserIntegrationToBeDisabled(t *testing.T) {
	uid := uint32(1000)
	executorUID := uint32(1002)
	layout := mcpLayout{
		RuntimeSocket: "/run/runtime.sock", PortGuardSocket: "/run/ports.sock",
		ExecutorSocket:    "/run/executor.sock",
		ExecutionContract: "/usr/share/doc/loki/execution-contract.json",
		PackagedSkillRoot: "/opt/loki/share/skills",
		RuntimeUID:        &uid, PortGuardUID: &uid, ExecutorUID: &executorUID,
	}
	options, err := layout.options("token")
	if err != nil {
		t.Fatal(err)
	}
	if options.Browser != nil || options.BrowserSocket != "" {
		t.Fatalf("disabled browser integration was constructed: %#v", options)
	}
	for _, mutate := range []func(*mcpLayout){
		func(l *mcpLayout) { l.BrowserSocket = "/run/browser.sock" },
		func(l *mcpLayout) { l.BrowserUID = &uid },
	} {
		changed := layout
		mutate(&changed)
		if _, err = changed.options("token"); err == nil {
			t.Fatal("partial browser peer configuration was accepted")
		}
	}
}

func TestMCPLayoutAllowsExecutorToRemainUnconfiguredBeforeJobSurfaceBinding(t *testing.T) {
	uid := uint32(1000)
	layout := mcpLayout{
		RuntimeSocket: "/run/runtime.sock", PortGuardSocket: "/run/ports.sock", BrowserSocket: "/run/browser.sock",
		ExecutionContract: "/usr/share/doc/loki/execution-contract.json", PackagedSkillRoot: "/opt/loki/share/skills",
		RuntimeUID: &uid, PortGuardUID: &uid, BrowserUID: &uid,
	}
	options, err := layout.options("token")
	if err != nil {
		t.Fatal(err)
	}
	if options.Jobs != nil || options.ExecutorSocket != "" {
		t.Fatalf("unexpected executor configuration = %#v", options)
	}
}

func TestMCPLayoutRejectsLauncherAuthority(t *testing.T) {
	for _, raw := range []string{
		`{"LauncherSocket":"/run/loki/launcher.sock"}`,
		`{"PolicySHA256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
	} {
		path := filepath.Join(t.TempDir(), "mcp.json")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		var layout mcpLayout
		if err := daemon.ReadJSON(path, &layout); err == nil {
			t.Fatalf("launcher authority field was accepted: %s", raw)
		}
	}
}

func TestServiceLayoutsCannotInjectPolicyGeneration(t *testing.T) {
	for _, test := range []struct {
		name string
		out  any
	}{
		{name: "mcp", out: &mcpLayout{}},
		{name: "runtime", out: &runtimeLayout{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "layout.json")
			if err := os.WriteFile(path, []byte(`{"PolicyGeneration":{"schema":1,"sha256":"forged"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := daemon.ReadJSON(path, test.out); err == nil {
				t.Fatal("caller-controlled policy generation was accepted")
			}
		})
	}
}
