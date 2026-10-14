package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"loki/internal/management"
	"loki/internal/tools"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNamedCodexBindingsSurviveDefaultAndRename(t *testing.T) {
	installSetupSSHFixture(t, `{"target":{"os":"linux","arch":"amd64","mode":"project-host"},"tools":{"browser":{"installed":true,"enabled":true}}}`)
	store := management.Store{Root: t.TempDir()}
	t.Setenv("CODEX_HOME", t.TempDir())
	config, err := defaultCodexConfig()
	if err != nil {
		t.Fatal(err)
	}
	user := "model = 'preserved'\n[mcp_servers.other]\ncommand = 'unchanged'\n"
	if err := os.WriteFile(config, []byte(user), 0600); err != nil {
		t.Fatal(err)
	}
	records := []management.HostRecord{}
	for _, name := range []string{"daily", "build"} {
		target := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: name + "@fixture"}, Root: "/home/fixture/" + name, Command: "loki"}
		record, err := store.RegisterHost(name, target)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
		if err := connectCodexOnHost(t.Context(), store, &target, []string{"codex"}, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	original, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(ownedCodexEntries(original)) != 2 {
		t.Fatal("connections did not coexist", string(original))
	}
	if err := store.UseHost("build"); err != nil {
		t.Fatal(err)
	}
	if err := store.RenameHost("daily", "renamed"); err != nil {
		t.Fatal(err)
	}
	if err := connectCodexOnHost(t.Context(), store, &records[0].Target, []string{"codex"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(config)
	if err != nil || !bytes.Equal(actual, original) {
		t.Fatal("rename/default switch rewrote bindings", string(actual), err)
	}
	if err := requireRegisteredHostDetached(store, records[0]); err == nil {
		t.Fatal("connected host removable")
	}
	if err := disconnectCodexTarget(store, &records[0].Target, "", false, io.Discard); err != nil {
		t.Fatal(err)
	}
	actual, err = os.ReadFile(config)
	if err != nil || !strings.HasPrefix(string(actual), user) || strings.Contains(string(actual), records[0].ID) || !strings.Contains(string(actual), records[1].ID) {
		t.Fatal("disconnect changed unrelated connection", string(actual), err)
	}
	if err := requireRegisteredHostDetached(store, records[0]); err != nil {
		t.Fatal("disconnected host blocked", err)
	}
	if err := store.RemoveHost(records[0].ID); err != nil {
		t.Fatal(err)
	}
}
func TestHostListIsReadOnlyAndScopedStatusKeepsReachableHosts(t *testing.T) {
	installSetupSSHFixture(t, `{}`)
	t.Setenv("LOKI_REVIEW_SSH_UNREACHABLE", "1")
	store := management.Store{Root: filepath.Join(t.TempDir(), "frontend")}
	var out bytes.Buffer
	if err := run(t.Context(), []string{"--root", store.Root, "hosts", "list"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.Root); !os.IsNotExist(err) {
		t.Fatal("listing created state", err)
	}
	if _, err := store.RegisterHost("offline", management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "offline@fixture"}, Command: "loki"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run(t.Context(), []string{"--root", store.Root, "--json", "status", "--all-hosts"}, &out, io.Discard); err == nil {
		t.Fatal("offline host reported complete")
	}
	var result struct {
		Complete bool
		Hosts    []hostStatusResult
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err, out.String())
	}
	if result.Complete || len(result.Hosts) != 2 {
		t.Fatal("missing partial results", out.String())
	}
	for _, h := range result.Hosts {
		if h.Name == "local" && (h.Error != "" || h.Report == nil) {
			t.Fatal("offline host blocked native", out.String())
		}
	}
	out.Reset()
	if err := run(t.Context(), []string{"--root", store.Root, "--host", "local", "status", "--all-hosts"}, &out, io.Discard); err == nil {
		t.Fatal("ambiguous scope accepted")
	}
}
func TestBridgeScopesAndLegacyDisconnect(t *testing.T) {
	store := management.Store{Root: t.TempDir()}
	a, err := store.RegisterHost("one", management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "a@fixture"}, Command: "loki"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.RegisterHost("two", management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "b@fixture"}, Command: "loki"})
	if err != nil {
		t.Fatal(err)
	}
	if connectionBridgeScope(store, a).Root == connectionBridgeScope(store, b).Root {
		t.Fatal("bridge credentials shared")
	}
	a.LegacyConnection = true
	if connectionBridgeScope(store, a).Root != store.Root {
		t.Fatal("legacy bridge credentials lost")
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	config, _ := defaultCodexConfig()
	raw := "# BEGIN Loki tools\n[mcp_servers.loki]\ncommand='loki'\nargs=['--host','ssh','--address','a@fixture','tools','serve']\n# END Loki tools\n"
	if err := os.WriteFile(config, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := disconnectCodexTarget(store, &b.Target, "", false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if actual, _ := os.ReadFile(config); string(actual) != raw {
		t.Fatal("other host disconnected")
	}
	if err := disconnectCodexTarget(store, &a.Target, "", false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if actual, _ := os.ReadFile(config); strings.Contains(string(actual), "mcp_servers.loki") {
		t.Fatal("legacy connection retained")
	}
}

func TestAllHostUpgradePublishesCLIOnceAndKeepsPartialResults(t *testing.T) {
	installSetupSSHFixture(t, `{"manager_version":"0.2.3","target":{"os":"linux","arch":"amd64","mode":"project-host"},"tools":{}}`)
	store := management.Store{Root: t.TempDir()}
	for _, name := range []string{"one", "two"} {
		if _, err := store.RegisterHost(name, management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: name + "@fixture"}, Command: "loki"}); err != nil {
			t.Fatal(err)
		}
	}
	deps, _, _ := upgradeFixture(t, "0.2.99", false)
	installs, applies := 0, 0
	deps.install = func(context.Context, management.Store, string, string, string) error { installs++; return nil }
	deps.apply = func(_ context.Context, _ string, _ management.Store, request upgradeApplyRequest, _ io.Reader, _ io.Writer) (upgradeApplyResult, error) {
		applies++
		if !request.All || len(request.Targets) != 2 || request.Targets[0].HostID == request.Targets[1].HostID {
			t.Fatalf("incorrect grouped request: %+v", request)
		}
		return upgradeApplyResult{Changed: true, Hosts: []hostUpgradeResult{{ID: request.Targets[0].HostID, Result: upgradeApplyResult{Changed: true}}, {ID: request.Targets[1].HostID, Error: "offline"}}}, fmt.Errorf("one host offline")
	}
	var out bytes.Buffer
	err := upgradeAllHostsWithDependencies(t.Context(), store, []string{"--all-hosts", "-y"}, strings.NewReader(""), &commandOutput{Writer: &out, json: true}, io.Discard, deps)
	if err == nil || installs != 1 || applies != 1 {
		t.Fatal("CLI publication repeated or partial error lost", installs, applies, err)
	}
	if value := singleJSON(t, out.Bytes()); value["state"] != "partial" {
		t.Fatal(out.String())
	}
}
func TestGroupedApplyContinuesAfterFailureAndIgnoresDefaultSwitch(t *testing.T) {
	store := management.Store{Root: t.TempDir()}
	registry, err := store.HostRegistry()
	if err != nil {
		t.Fatal(err)
	}
	native := registry.Selected()
	report, err := inspectOperatorHost(t.Context(), store, &native.Target, io.Discard, false)
	if err != nil {
		t.Fatal(err)
	}
	basis, err := upgradeToolsBasis(*report)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := store.RegisterHost("other", management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "other@fixture"}, Command: "loki"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UseHost(remote.ID); err != nil {
		t.Fatal(err)
	}
	good := upgradeApplyRequest{Version: management.ManagerRelease, HostID: native.ID, Selection: &native.Target, Basis: basis}
	bad := good
	bad.Basis = strings.Repeat("f", 64)
	applied, err := applyUpgradeGroup(t.Context(), store, upgradeApplyRequest{All: true, Targets: []upgradeApplyRequest{bad, good}}, io.Discard)
	if err == nil || len(applied.Hosts) != 2 || applied.Hosts[0].Error == "" || applied.Hosts[1].Error != "" {
		t.Fatal("failure blocked other host or default retargeted apply", applied, err)
	}
}

func TestHostRegistrationCommandParsesNamedOptions(t *testing.T) {
	store := management.Store{Root: t.TempDir()}
	var out bytes.Buffer
	if err := run(t.Context(), []string{"--root", store.Root, "hosts", "add", "work", "--type", "wsl", "--distribution", "Ubuntu", "--root", "/home/alice/state", "--user", "alice", "--json"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	record, err := store.RegisteredHost("work")
	if err != nil || record.Target.Root != "/home/alice/state" || record.Target.User != "alice" {
		t.Fatal(record, err)
	}
	if err := run(t.Context(), []string{"--root", store.Root, "hosts", "use", "work"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run(t.Context(), []string{"--root", store.Root, "connections", "list", "--json"}, &out, io.Discard); err != nil {
		t.Fatal("listing connections required a provider", err)
	}
	if err := run(t.Context(), []string{"--root", store.Root, "--host", "work", "--address", "ignored@host", "status"}, io.Discard, io.Discard); err == nil {
		t.Fatal("conflicting transport ignored")
	}
}

func TestBridgeAuthorizationTracksBoundInstallation(t *testing.T) {
	store := management.Store{Root: t.TempDir()}
	record, err := store.RegisterHost("bound", management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "bound@fixture"}, Command: "loki"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UseHost("local"); err != nil {
		t.Fatal(err)
	}
	if err := store.RenameHost("bound", "renamed"); err != nil {
		t.Fatal(err)
	}
	if err := authorizeConnectionHost(store, record); err != nil {
		t.Fatal("default/rename revoked the bound connection", err)
	}
	next := record.Target
	next.Root = "/new/installation"
	if err := store.UpdateHostTarget(record.ID, record.Target, next); err != nil {
		t.Fatal(err)
	}
	if err := authorizeConnectionHost(store, record); err == nil {
		t.Fatal("old bridge admitted a different installation")
	}
	if err := store.RemoveHost(record.ID); err != nil {
		t.Fatal(err)
	}
	if err := authorizeConnectionHost(store, record); err == nil {
		t.Fatal("removed host remained authorized")
	}
}
func TestSharedWSLInfrastructureCannotBePurged(t *testing.T) {
	store := management.Store{Root: t.TempDir()}
	t.Setenv("CODEX_HOME", t.TempDir())
	a := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "wsl", Distribution: "Loki-Test"}, Root: "/var/lib/loki-tools", Command: "loki", User: "root", Owned: true}
	if _, err := store.RegisterHost("owned", a); err != nil {
		t.Fatal(err)
	}
	b := a
	b.Root = "/another/root"
	b.Host.Distribution = "loki-test"
	b.Owned = false
	if _, err := store.RegisterHost("shared", b); err != nil {
		t.Fatal(err)
	}
	if err := runHosts(t.Context(), store, []string{"remove", "owned", "--purge"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "also referenced") {
		t.Fatal("shared infrastructure removal attempted", err)
	}
	if _, err := store.RegisteredHost("owned"); err != nil {
		t.Fatal("failed purge detached host", err)
	}
}

func TestLegacySystemConnectionIsNotNative(t *testing.T) {
	store := management.Store{Root: t.TempDir()}
	t.Setenv("CODEX_HOME", t.TempDir())
	config, _ := defaultCodexConfig()
	raw := []byte("# BEGIN Loki tools\n[mcp_servers.loki]\ncommand='loki'\nargs=['--host','local','--system-socket','/run/loki-tools-manager-1000.sock','--root','/var/lib/loki-tools/1000','tools','serve']\n# END Loki tools\n")
	if err := os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := disconnectCodexTarget(store, nil, "", false, io.Discard); err != nil {
		t.Fatal(err)
	}
	actual, _ := os.ReadFile(config)
	if !bytes.Equal(actual, raw) {
		t.Fatal("native disconnect removed system host connection")
	}
}
func TestDisconnectRejectsChangedSelectionSnapshot(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.toml")
	old := []byte("# BEGIN Loki tools\n[mcp_servers.loki]\ncommand='old'\n# END Loki tools\n")
	next := bytes.ReplaceAll(old, []byte("'old'"), []byte("'other-host'"))
	if err := os.WriteFile(config, next, 0600); err != nil {
		t.Fatal(err)
	}
	if err := disconnectCodexEntries(config, old, ownedCodexEntries(old), io.Discard); err == nil {
		t.Fatal("changed connection was removed")
	}
	actual, _ := os.ReadFile(config)
	if !bytes.Equal(actual, next) {
		t.Fatal("new connection altered")
	}
}
func TestRegisteredSetupArgumentsRetainFrontendAndOwnership(t *testing.T) {
	store := management.Store{Root: t.TempDir()}
	target := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "wsl", Distribution: "loki-tools-test"}, Root: "/var/lib/loki-tools", Command: "/usr/local/bin/loki", User: "root", Owned: true}
	record, err := store.RegisterHost("managed", target)
	if err != nil {
		t.Fatal(err)
	}
	args := setupBoundArguments(store, target)
	var out bytes.Buffer
	if err := run(t.Context(), append(args, "hosts", "show", "managed", "--json"), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var actual management.HostRecord
	if err := json.Unmarshal(out.Bytes(), &actual); err != nil {
		t.Fatal(err)
	}
	if actual.ID != record.ID || actual.Target != target {
		t.Fatal("bound invocation lost frontend/ownership", actual)
	}
}

func TestGroupedUpgradeRejectsOldProtocolBeforePublishing(t *testing.T) {
	deps, _, _ := upgradeFixture(t, "0.2.99", false)
	deps.plan = func(context.Context, management.Store, string, io.Writer) (*upgradeToolsPlan, error) {
		return &upgradeToolsPlan{All: true, Update: true, Basis: strings.Repeat("a", 64)}, nil
	}
	originalRun := deps.run
	deps.run = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "_upgrade-apply" {
			return []byte("-request"), nil
		}
		return originalRun(ctx, binary, args...)
	}
	published := false
	deps.install = func(context.Context, management.Store, string, string, string) error { published = true; return nil }
	err := runUpgrade(t.Context(), management.Store{Root: t.TempDir()}, []string{"-y"}, strings.NewReader(""), io.Discard, io.Discard, deps)
	if err == nil || published || !strings.Contains(err.Error(), "multiple-host upgrades") {
		t.Fatal("unsupported candidate published before protocol validation", published, err)
	}
}
func TestBridgeBindingRequiresCurrentTarget(t *testing.T) {
	record := management.HostRecord{ID: strings.Repeat("a", 32), Target: management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "user@host"}, Command: "loki"}}
	binding := connectionBridgeBinding{ID: record.ID, Target: record.Target}
	record.Target.Root = "/prepared/root"
	if binding.matches(record) {
		t.Fatal("bridge for pre-preparation target was reused")
	}
}

func TestOwnedWSLReuseRequiresRecordedProof(t *testing.T) {
	store := management.Store{Root: t.TempDir()}
	selected := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "wsl", Distribution: "loki-tools-test"}, Command: "loki", Owned: true}
	calls := 0
	matching := false
	run := func(_ context.Context, probe management.ExecutionSelection, args []string, _ io.Reader, out, _ io.Writer) error {
		calls++
		if probe.Command != "/usr/bin/cat" || len(args) != 1 || args[0] != "/var/lib/loki-host-owner" {
			t.Fatal("ownership probe not used")
		}
		if matching {
			fmt.Fprint(out, strings.Repeat("a", 64))
		}
		return nil
	}
	if reuse, err := verifiedOwnedWSLHost(t.Context(), store, selected, run); err != nil || reuse || calls != 0 {
		t.Fatal("missing ownership adopted", reuse, err, calls)
	}
	if err := store.SaveHostPreparation(management.HostPreparation{Schema: 1, Distribution: selected.Host.Distribution, Token: strings.Repeat("a", 64), Phase: "ready"}); err != nil {
		t.Fatal(err)
	}
	if reuse, err := verifiedOwnedWSLHost(t.Context(), store, selected, run); err == nil || reuse {
		t.Fatal("ownership mismatch adopted", reuse, err)
	}
	matching = true
	if reuse, err := verifiedOwnedWSLHost(t.Context(), store, selected, run); err != nil || !reuse {
		t.Fatal("verified host not reusable", reuse, err)
	}
}

func TestLegacyNativeConnectionKeepsItsStateRoot(t *testing.T) {
	store := management.Store{Root: filepath.Join(t.TempDir(), "current")}
	t.Setenv("CODEX_HOME", t.TempDir())
	config, _ := defaultCodexConfig()
	root := filepath.Join(t.TempDir(), "other")
	raw := []byte("# BEGIN Loki tools\n[mcp_servers.loki]\ncommand='loki'\nargs=['--host','local','--root','" + filepath.ToSlash(root) + "','tools','serve']\n# END Loki tools\n")
	if err := os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := disconnectCodexTarget(store, nil, "", false, io.Discard); err != nil {
		t.Fatal(err)
	}
	actual, _ := os.ReadFile(config)
	if !bytes.Equal(actual, raw) {
		t.Fatal("native disconnect removed another state root's connection")
	}
}

func TestUnknownCommandDoesNotContactRegisteredHost(t *testing.T) {
	log := installSetupSSHFixture(t, `{}`)
	store := management.Store{Root: t.TempDir()}
	target := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "user@fixture"}, Command: "loki"}
	if err := store.SelectExecutionHost(target); err != nil {
		t.Fatal(err)
	}
	err := run(t.Context(), []string{"--root", store.Root, "unknown"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "loki --help") {
		t.Fatal("local command error was forwarded", err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("unknown command contacted remote host", err)
	}
}

func TestHostPurgeRequiresExplicitName(t *testing.T) {
	store := management.Store{Root: t.TempDir()}
	err := runHosts(t.Context(), store, []string{"remove", "--purge"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "name the host before deletion") {
		t.Fatal("unnamed purge reached host removal", err)
	}
	if _, err := os.Stat(store.ControlDirectory()); !os.IsNotExist(err) {
		t.Fatal("invalid purge created state", err)
	}
}

func TestAllHostDoctorRetainsReachableDegradedTools(t *testing.T) {
	installSetupSSHFixture(t, `{"target":{"os":"linux","arch":"amd64","mode":"project-host"},"ready":false,"healthy":false,"tools":{"browser":{"installed":true,"enabled":true,"readiness":"degraded","reason":"missing runtime library"}}}`)
	t.Setenv("LOKI_REVIEW_SSH_DOCTOR_FAIL", "1")
	store := management.Store{Root: t.TempDir()}
	if _, err := store.RegisterHost("degraded", management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "fixture@host"}, Command: "loki"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := statusAllHosts(t.Context(), store, &out, io.Discard, true)
	if err == nil || !strings.Contains(out.String(), "browser: needs attention") || !strings.Contains(out.String(), "missing runtime library") || strings.Contains(out.String(), "Unavailable:") {
		t.Fatal("reachable tool failure misreported as unavailable", out.String(), err)
	}
}
