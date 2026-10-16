package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/management"
	"loki/internal/tools"
)

func TestUpgradeApplyProtocolProbeUsesInternalParser(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absent")
	var out, diagnostics bytes.Buffer
	err := run(t.Context(), []string{"--root", root, "_upgrade-apply", "--help"}, &out, &diagnostics)
	if !errors.Is(err, flag.ErrHelp) || !strings.Contains(diagnostics.String(), "request") {
		t.Fatalf("internal upgrade protocol not available: %v %s", err, diagnostics.String())
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("protocol probe created state", err)
	}
}

func catalogTestClient(t *testing.T, catalog tools.Catalog) *http.Client {
	t.Helper()
	data, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	target := catalog.Artifacts[0].Target
	asset := fmt.Sprintf("loki-catalog-%s-%s-%s.json", target.OS, target.Arch, target.Mode)
	return &http.Client{Transport: catalogRoundTripper(func(request *http.Request) (*http.Response, error) {
		body := data
		if strings.HasSuffix(request.URL.Path, "/SHA256SUMS") {
			body = []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(data), asset))
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})}
}

func TestIntegratedUpgradeScopesAndReadOnlyPlanning(t *testing.T) {
	for _, test := range []struct {
		name                         string
		args                         []string
		input                        string
		wantPlan, wantCLI, wantApply bool
	}{
		{"all", []string{"-y"}, "", true, true, true},
		{"check", []string{"--check"}, "", true, false, false},
		{"cancel", nil, "n\n", true, false, false},
		{"CLI only", []string{"--cli-only", "-y"}, "", false, true, false},
		{"tools only", []string{"--tools-only", "-y"}, "", true, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			deps, _, _ := upgradeFixture(t, "0.2.99", false)
			planned, installed, applied := false, false, false
			deps.plan = func(context.Context, management.Store, string, io.Writer) (*upgradeToolsPlan, error) {
				planned = true
				return &upgradeToolsPlan{Host: "WSL: Ubuntu", Update: true, Installed: 1, ManagerCurrent: management.ManagerRelease, ReleaseCurrent: management.Release, Basis: strings.Repeat("a", 64)}, nil
			}
			deps.install = func(context.Context, management.Store, string, string, string) error { installed = true; return nil }
			deps.apply = func(_ context.Context, _ string, _ management.Store, r upgradeApplyRequest, _ io.Reader, _ io.Writer) (upgradeApplyResult, error) {
				applied = true
				if r.Version != "0.2.99" || r.Basis != strings.Repeat("a", 64) {
					t.Fatal(r)
				}
				return upgradeApplyResult{Changed: true, Reconnect: true}, nil
			}
			root := filepath.Join(t.TempDir(), "absent")
			var out bytes.Buffer
			if err := runUpgrade(t.Context(), management.Store{Root: root}, test.args, strings.NewReader(test.input), &out, io.Discard, deps); err != nil {
				t.Fatal(err)
			}
			if planned != test.wantPlan || installed != test.wantCLI || applied != test.wantApply {
				t.Fatalf("plan=%v CLI=%v apply=%v", planned, installed, applied)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatal("planning or mocked publication created state", err)
			}
		})
	}
	deps, requests, _ := upgradeFixture(t, "0.2.99", false)
	if err := runUpgrade(t.Context(), management.Store{Root: t.TempDir()}, []string{"--cli-only", "--tools-only"}, strings.NewReader(""), io.Discard, io.Discard, deps); err == nil || len(*requests) != 0 {
		t.Fatal("conflicting scopes contacted network", err)
	}
}

func TestIntegratedUpgradeContinuesWhenCLIIsCurrent(t *testing.T) {
	deps, _, _ := upgradeFixture(t, management.ManagerRelease, false)
	applied, installed := false, false
	deps.plan = func(context.Context, management.Store, string, io.Writer) (*upgradeToolsPlan, error) {
		return &upgradeToolsPlan{Update: true, Basis: strings.Repeat("a", 64)}, nil
	}
	deps.run = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	deps.install = func(context.Context, management.Store, string, string, string) error { installed = true; return nil }
	deps.apply = func(context.Context, string, management.Store, upgradeApplyRequest, io.Reader, io.Writer) (upgradeApplyResult, error) {
		applied = true
		return upgradeApplyResult{Changed: true}, nil
	}
	var out bytes.Buffer
	if err := runUpgrade(t.Context(), management.Store{Root: t.TempDir()}, []string{"-y"}, strings.NewReader(""), &out, io.Discard, deps); err != nil || !applied || installed {
		t.Fatalf("apply=%v CLI=%v err=%v", applied, installed, err)
	}
}

func TestIntegratedUpgradePartialJSONKeepsAppliedState(t *testing.T) {
	deps, _, _ := upgradeFixture(t, "0.2.99", false)
	deps.plan = func(context.Context, management.Store, string, io.Writer) (*upgradeToolsPlan, error) {
		return &upgradeToolsPlan{Update: true, Basis: strings.Repeat("a", 64), Clients: true}, nil
	}
	deps.apply = func(context.Context, string, management.Store, upgradeApplyRequest, io.Reader, io.Writer) (upgradeApplyResult, error) {
		return upgradeApplyResult{Changed: true, Reconnect: true}, errors.New("service replacement failed")
	}
	var out, diagnostics bytes.Buffer
	err := runUpgrade(t.Context(), management.Store{Root: t.TempDir()}, []string{"-y"}, strings.NewReader(""), &commandOutput{Writer: &out, json: true}, &diagnostics, deps)
	if err == nil {
		t.Fatal("partial failure claimed success")
	}
	value := singleJSON(t, out.Bytes())
	if value["state"] != "partial" || value["cli_changed"] != true || value["changed"] != true || value["reconnect_required"] != true {
		t.Fatal(value)
	}
}

func TestUserHostUpgradePreservesCustomOwnedCommandAndRoot(t *testing.T) {
	for _, modern := range []bool{false, true} {
		t.Run(fmt.Sprint(modern), func(t *testing.T) {
			selected := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "fixture@host"}, Command: "/custom command/bin/loki", Root: "/custom state"}
			var calls [][]string
			err := upgradeUserHostManager(t.Context(), selected, "0.2.99", true, io.Discard, func(_ context.Context, got management.ExecutionSelection, args []string, _ io.Reader, out, diagnostics io.Writer) error {
				if got != selected {
					t.Fatal("host or command redirected", got)
				}
				calls = append(calls, append([]string(nil), args...))
				if len(calls) == 1 {
					if modern {
						fmt.Fprintln(out, "--cli-only")
					}
					return nil
				}
				if len(calls) == 3 {
					fmt.Fprintln(out, "loki 0.2.99")
				}
				return nil
			})
			if err != nil || len(calls) != 3 {
				t.Fatal(calls, err)
			}
			command := strings.Join(calls[1], "|")
			if !strings.Contains(command, "--host|local|--root|/custom state|upgrade|--version|0.2.99|--yes") || strings.Contains(command, "--cli-only") != modern {
				t.Fatal(calls)
			}
			if !strings.Contains(command, "--force") {
				t.Fatal("approved downgrade flag lost", calls)
			}
		})
	}
}

func TestUpgradeApplyRejectsHostAndToolChangesBeforeMutation(t *testing.T) {
	store := management.Store{Root: t.TempDir()}
	report, err := store.Status()
	if err != nil {
		t.Fatal(err)
	}
	basis, err := upgradeToolsBasis(report)
	if err != nil {
		t.Fatal(err)
	}
	selected := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "fixture@host"}, Command: "loki", Root: "/state"}
	if err := store.SelectExecutionHost(selected); err != nil {
		t.Fatal(err)
	}
	if _, err := applyUpgradeTools(t.Context(), store, upgradeApplyRequest{Version: management.ManagerRelease, Basis: basis, Remember: true}, io.Discard); err == nil || !strings.Contains(err.Error(), "host changed") {
		t.Fatal(err)
	}
	if _, err := applyUpgradeTools(t.Context(), store, upgradeApplyRequest{Version: management.ManagerRelease, Basis: strings.Repeat("a", 64)}, io.Discard); err == nil || !strings.Contains(err.Error(), "changed after confirmation") {
		t.Fatal(err)
	}
}

func upgradeTestCatalog(target tools.Target, digest string) tools.Catalog {
	artifact := tools.Artifact{Module: "browser", Release: "0.2.98", Target: target, URL: "https://example.invalid/browser.zip", SHA256: digest, Bytes: 1, Format: "zip"}
	manifest := tools.Manifest{Schema: 1, ID: "browser", Release: artifact.Release, Contract: tools.CompositionContract, Targets: []tools.Target{target}, Tools: []string{}}
	return tools.Catalog{Schema: 1, Release: "0.2.99", Contract: tools.CompositionContract, Modules: []tools.Manifest{manifest}, Artifacts: []tools.Artifact{artifact}}
}

func TestUpgradeUsesDigestsAndDetectsPendingRuntime(t *testing.T) {
	target := tools.Target{OS: "linux", Arch: "amd64", Mode: tools.Full}
	catalog := upgradeTestCatalog(target, strings.Repeat("a", 64))
	report := management.Report{Release: "0.2.98", Target: target, Tools: map[tools.ID]tools.State{"browser": {Installed: true, Enabled: true, Release: "0.2.98", SHA256: strings.Repeat("a", 64)}}}
	changed, err := changedUpgradeTools(report, catalog)
	if err != nil || len(changed) != 0 {
		t.Fatal("reused package incorrectly marked changed", changed, err)
	}
	report.Deployments = []management.DeploymentReservation{{Artifacts: catalog.Artifacts}}
	if needed, err := upgradeRuntimeNeeded(report, catalog); err != nil || needed {
		t.Fatal("unchanged running packages need restart", err)
	}
	report.Deployments[0].Artifacts = append([]tools.Artifact(nil), catalog.Artifacts...)
	report.Deployments[0].Artifacts[0].SHA256 = strings.Repeat("b", 64)
	if needed, err := upgradeRuntimeNeeded(report, catalog); err != nil || !needed {
		t.Fatal("stale running packages ignored after files updated", err)
	}
	report.Deployments = nil
	if needed, err := upgradeRuntimeNeeded(report, catalog); err != nil || needed {
		t.Fatal("stopped service would start", err)
	}
	old := report.Tools["browser"]
	old.SHA256 = ""
	report.Tools["browser"] = old
	changed, err = changedUpgradeTools(report, catalog)
	if err != nil || len(changed) != 1 || !changed[0].VerificationOnly {
		t.Fatal("older host metadata falsely claimed package equality", changed, err)
	}
}

func TestIntegratedPlanReadsCatalogWithoutCreatingResources(t *testing.T) {
	store := management.Store{Root: t.TempDir()}
	catalog := upgradeTestCatalog(management.LocalTarget(tools.ProjectHost), strings.Repeat("a", 64))
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.Config.Contract = tools.CompositionContract
	state.Config.Release = "0.2.98"
	state.Installed["browser"] = management.Installation{Artifact: catalog.Artifacts[0], Manifest: catalog.Modules[0]}
	state.Config.Tools = []tools.Selection{{ID: "browser", Enabled: true}}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.ActivationPath())
	if err != nil {
		t.Fatal(err)
	}
	client := catalogTestClient(t, catalog)
	plan, err := planUpgradeTools(t.Context(), store, nil, true, "0.2.99", io.Discard, client)
	if err != nil || plan.Update || len(plan.Changes) != 0 || plan.RuntimeUpdate {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	after, _ := os.ReadFile(store.ActivationPath())
	if !bytes.Equal(before, after) {
		t.Fatal("check mutated activation")
	}
	if _, err := os.Stat(filepath.Join(store.Root, "tools")); !os.IsNotExist(err) {
		t.Fatal("check prepared resources", err)
	}
}

func TestAlreadyCurrentUpgradeRunsHealthWithoutPublishing(t *testing.T) {
	for _, check := range []bool{false, true} {
		deps, _, _ := upgradeFixture(t, management.ManagerRelease, false)
		deps.plan = func(context.Context, management.Store, string, io.Writer) (*upgradeToolsPlan, error) {
			return &upgradeToolsPlan{Installed: 1, Basis: strings.Repeat("a", 64)}, nil
		}
		deps.run = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
		deps.install = func(context.Context, management.Store, string, string, string) error {
			t.Fatal("already current CLI was installed again")
			return nil
		}
		calls := 0
		deps.apply = func(context.Context, string, management.Store, upgradeApplyRequest, io.Reader, io.Writer) (upgradeApplyResult, error) {
			calls++
			return upgradeApplyResult{Health: &upgradeHealth{State: "passed"}}, nil
		}
		var out bytes.Buffer
		args := []string{}
		if check {
			args = append(args, "--check")
		}
		if err := runUpgrade(t.Context(), management.Store{Root: t.TempDir()}, args, strings.NewReader(""), &commandOutput{Writer: &out, json: true}, io.Discard, deps); err != nil {
			t.Fatal(err)
		}
		value := singleJSON(t, out.Bytes())
		if check && calls != 0 || !check && calls != 1 || value["changed"] != false {
			t.Fatal("read-only planning probed tools or current CLI was republished", calls, value)
		}
		if !check && value["applied"].(map[string]any)["health"].(map[string]any)["state"] != "passed" {
			t.Fatal(value)
		}
	}
}

func TestAllHostUpgradeDiagnosesCurrentHostsAlongsideUpdates(t *testing.T) {
	deps, _, _ := upgradeFixture(t, "0.2.99", false)
	deps.plan = func(context.Context, management.Store, string, io.Writer) (*upgradeToolsPlan, error) {
		return &upgradeToolsPlan{All: true, Update: true, Basis: strings.Repeat("c", 64), Hosts: []*upgradeToolsPlan{
			{HostID: "current", Installed: 1, Basis: strings.Repeat("a", 64)},
			{HostID: "update", Update: true, Installed: 1, Basis: strings.Repeat("b", 64)},
		}}, nil
	}
	deps.apply = func(_ context.Context, _ string, _ management.Store, request upgradeApplyRequest, _ io.Reader, _ io.Writer) (upgradeApplyResult, error) {
		if !request.All || len(request.Targets) != 2 || request.Targets[0].HostID != "current" || request.Targets[1].HostID != "update" {
			t.Fatal("already-current host health was omitted", request)
		}
		return upgradeApplyResult{}, nil
	}
	if err := runUpgrade(t.Context(), management.Store{Root: t.TempDir()}, []string{"-y"}, strings.NewReader(""), io.Discard, io.Discard, deps); err != nil {
		t.Fatal(err)
	}
}
