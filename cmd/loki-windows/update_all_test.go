package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestProductUpdateAllOptions(t *testing.T) {
	for _, args := range [][]string{{"--all"}, {"--all", "--distribution", "custom", "--interrupt-active-jobs"}} {
		options, err := parseProductUpdateOptions(args, "loki-mcp")
		if err != nil || !options.All {
			t.Fatalf("options=%+v err=%v", options, err)
		}
		if len(args) == 1 && (options.Distribution != "loki-mcp" || options.InterruptActiveJobs) {
			t.Fatalf("default options=%+v", options)
		}
		if len(args) > 1 && (options.Distribution != "custom" || !options.InterruptActiveJobs) {
			t.Fatalf("explicit options=%+v", options)
		}
	}
	for _, args := range [][]string{
		{"--all=false"}, {"--distribution", "custom"}, {"--interrupt-active-jobs"},
		{"--all", "extra"}, {"--all", "--json"}, {"--all", "--approve"},
		{"--all", "--distribution", "../other"}, {"--all", "--distribution"},
	} {
		if _, err := parseProductUpdateOptions(args, "loki-mcp"); err == nil {
			t.Fatalf("invalid combined update accepted: %v", args)
		}
	}
}

func TestAllUpdateSequencingAndFailureRecovery(t *testing.T) {
	for _, scenario := range []string{"update", "current", "newer", "prepared", "stale-prepared", "status-fails", "prepare-fails", "apply-fails", "changed-release", "invalid-status", "wrong-applied-release", "interrupt", "doctor-fails", "resync-fails", "startup-fails"} {
		t.Run(scenario, func(t *testing.T) {
			installed := machineGenerationForUpdateTest("0.1.39")
			available := machineGenerationForUpdateTest("0.1.40")
			var prepared *machinePreparedPlan
			if scenario == "current" || scenario == "doctor-fails" || scenario == "resync-fails" || scenario == "startup-fails" {
				installed = machineGenerationForUpdateTest("0.1.40")
			}
			if scenario == "newer" {
				installed = machineGenerationForUpdateTest("0.1.41")
			}
			if scenario == "prepared" || scenario == "stale-prepared" {
				prepared = &machinePreparedPlan{ID: "saved-plan"}
				if scenario == "stale-prepared" {
					available = machineGenerationForUpdateTest("0.1.39")
				}
			}
			options := productUpdateOptions{All: true, Distribution: "custom", InterruptActiveJobs: scenario == "interrupt"}
			var calls []string
			deps := allUpdateDependencies{Executable: "updated.exe", ReleaseTag: "v0.1.40"}
			deps.Run = func(_ context.Context, executable string, args []string, stdout, stderr io.Writer) int {
				if args[0] == "connect" {
					if executable != "updated.exe" || !slices.Equal(args, []string{"connect", "startup", "--distribution", "custom"}) {
						t.Fatalf("startup=%s args=%v", executable, args)
					}
					calls = append(calls, "startup")
					if scenario == "startup-fails" {
						return 12
					}
					return 0
				}
				if args[0] == "doctor" {
					if executable != "updated.exe" || !slices.Equal(args, []string{"doctor", "--distribution", "custom"}) {
						t.Fatalf("doctor=%s args=%v", executable, args)
					}
					calls = append(calls, "doctor")
					if scenario == "doctor-fails" {
						return 10
					}
					return 0
				}
				if args[0] == "connection" {
					if executable != "updated.exe" || !slices.Equal(args, []string{"connection", "show", "--distribution", "custom", "local"}) {
						t.Fatalf("resync=%s args=%v", executable, args)
					}
					calls = append(calls, "resync")
					if scenario == "resync-fails" {
						return 11
					}
					return 0
				}
				if executable != "updated.exe" || len(args) < 4 || !slices.Equal(args[:1], []string{"update"}) || !slices.Equal(args[2:4], []string{"--distribution", "custom"}) {
					t.Fatalf("executable=%s args=%v", executable, args)
				}
				action := args[1]
				calls = append(calls, action)
				switch action {
				case "status":
					if !slices.Equal(args[4:], []string{"--json"}) {
						t.Fatalf("status args=%v", args)
					}
					if scenario == "status-fails" {
						return 7
					}
					if scenario == "invalid-status" {
						io.WriteString(stdout, `{"installed":null}`)
						return 0
					}
					json.NewEncoder(stdout).Encode(machineUpdateStatus{Installed: installed, Available: available, Prepared: prepared, UpdateAvailable: installed.Spec.Version != available.Spec.Version})
				case "prepare":
					if len(args) != 4 {
						t.Fatalf("prepare args=%v", args)
					}
					if scenario == "prepare-fails" {
						return 8
					}
					available = machineGenerationForUpdateTest("0.1.40")
					if scenario == "changed-release" {
						available = machineGenerationForUpdateTest("0.1.41")
					}
					prepared = &machinePreparedPlan{ID: "new-plan"}
				case "apply":
					want := []string{"--approve"}
					if options.InterruptActiveJobs {
						want = append(want, "--interrupt-active-jobs")
					}
					if !slices.Equal(args[4:], want) || prepared == nil {
						t.Fatalf("apply args=%v prepared=%v", args, prepared)
					}
					if scenario == "apply-fails" {
						return 9
					}
					if scenario != "wrong-applied-release" {
						installed = available
					}
					prepared = nil
				default:
					t.Fatalf("unexpected action %s", action)
				}
				return 0
			}
			var stdout, stderr bytes.Buffer
			code := runAllApplianceUpdateWith(t.Context(), deps, options, &stdout, &stderr)
			wantCalls := []string{"status", "prepare", "status", "apply", "status"}
			wantCode := 0
			if scenario == "newer" || scenario == "status-fails" || scenario == "invalid-status" {
				wantCalls = []string{"status"}
			}
			if scenario == "current" {
				wantCalls = []string{"status", "resync", "startup", "doctor"}
			}
			if scenario == "prepared" {
				wantCalls = []string{"status", "apply", "status"}
			}
			switch scenario {
			case "doctor-fails":
				wantCalls, wantCode = []string{"status", "resync", "startup", "doctor"}, 10
			case "resync-fails":
				wantCalls, wantCode = []string{"status", "resync"}, 11
			case "startup-fails":
				wantCalls, wantCode = []string{"status", "resync", "startup"}, 12
			case "status-fails":
				wantCode = 7
			case "prepare-fails":
				wantCalls, wantCode = []string{"status", "prepare"}, 8
			case "apply-fails":
				wantCalls, wantCode = []string{"status", "prepare", "status", "apply"}, 9
			case "changed-release":
				wantCalls, wantCode = []string{"status", "prepare", "status"}, 1
			case "invalid-status", "wrong-applied-release":
				wantCode = 1
			}
			if code != wantCode || !slices.Equal(calls, wantCalls) {
				t.Fatalf("code=%d want=%d calls=%v want=%v stderr=%s", code, wantCode, calls, wantCalls, stderr.String())
			}
			if code != 0 && (!strings.Contains(stderr.String(), "frontend remains updated") || !strings.Contains(stderr.String(), "--distribution custom")) {
				t.Fatalf("missing recovery guidance: %s", stderr.String())
			}
		})
	}
}

func TestAllUpdateCancellationPreventsApplianceMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	deps := allUpdateDependencies{Executable: "updated.exe", ReleaseTag: "v0.1.40", Run: func(context.Context, string, []string, io.Writer, io.Writer) int {
		t.Fatal("cancelled update executed an appliance command")
		return 0
	}}
	if code := runAllApplianceUpdateWith(ctx, deps, productUpdateOptions{All: true, Distribution: "custom"}, io.Discard, io.Discard); code == 0 {
		t.Fatal("cancelled update reported success")
	}
}
