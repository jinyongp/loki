package appliance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/host/diagnostics"
)

type fixture struct {
	host      Host
	versions  map[string]string
	calls     []string
	unhealthy map[string]bool
	failed    string
	aptError  bool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{versions: map[string]string{}, unhealthy: map[string]bool{}}
	f.host = Host{Root: t.TempDir()}
	for _, item := range requirements() {
		f.versions[item.Package] = item.Version
		f.write(t, item.Path, item.Kind)
	}
	f.write(t, "/usr/lib/loki-appliance/release-manifest.json", "file")
	f.write(t, "/var/lib/systemd/linger/ubuntu", "file")
	f.write(t, "/proc/sys/kernel/osrelease", "file")
	if err := os.WriteFile(f.host.path("/proc/sys/kernel/osrelease"), []byte("6.18-microsoft-standard-WSL2"), 0644); err != nil {
		t.Fatal(err)
	}
	f.host.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		f.calls = append(f.calls, filepath.Base(name)+" "+strings.Join(args, " "))
		switch filepath.Base(name) {
		case "dpkg-query":
			version := f.versions[args[len(args)-1]]
			if version == "" {
				return "", errors.New("package absent")
			}
			return "installed\t" + version, nil
		case "runuser":
			if f.unhealthy["user@1000.service"] {
				return "inactive", errors.New("user session absent")
			}
			return "active", nil
		case "loginctl":
			if strings.Join(args, " ") != "enable-linger ubuntu" {
				t.Fatalf("unexpected loginctl mutation: %v", args)
			}
			f.write(t, "/var/lib/systemd/linger/ubuntu", "file")
		case "apt-get":
			if f.aptError {
				return "", errors.New("repository unavailable")
			}
			if args[0] == "install" {
				for _, arg := range args[1:] {
					name, version, ok := strings.Cut(arg, "=")
					if !ok {
						continue
					}
					f.versions[name] = version
					for _, item := range requirements() {
						if item.Package == name {
							f.write(t, item.Path, item.Kind)
						}
					}
				}
			}
		case "systemctl":
			switch args[0] {
			case "--failed":
				return f.failed, nil
			case "show":
				if args[len(args)-1] == "--value" {
					if f.unhealthy[args[1]] {
						return "failed", nil
					}
					return "active", nil
				}
				if f.unhealthy[args[1]] {
					return "LoadState=loaded\nActiveState=failed\nResult=exit-code\nConditionResult=yes", nil
				}
				return "LoadState=loaded\nActiveState=active\nResult=success\nConditionResult=yes", nil
			case "start":
				delete(f.unhealthy, args[1])
			}
		}
		return "", nil
	}
	return f
}

func (f *fixture) write(t *testing.T, path, kind string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.host.path(path)), 0755); err != nil {
		t.Fatal(err)
	}
	mode := os.FileMode(0644)
	if kind == "executable" {
		mode = 0755
	}
	if err := os.WriteFile(f.host.path(path), []byte("fixture"), mode); err != nil {
		t.Fatal(err)
	}
}

func healthy(t *testing.T, f *fixture) bool {
	t.Helper()
	report, err := diagnostics.NewReport(time.Now().UTC(), f.host.Inspect(t.Context())...)
	if err != nil {
		t.Fatal(err)
	}
	return report.Healthy()
}

func TestManagedRequiresBothImageIdentityAndWSL(t *testing.T) {
	f := newFixture(t)
	if managed, err := f.host.Managed(); !managed || err != nil {
		t.Fatalf("managed=%v err=%v", managed, err)
	}
	if err := os.WriteFile(f.host.path("/proc/sys/kernel/osrelease"), []byte("6.18-generic"), 0644); err != nil {
		t.Fatal(err)
	}
	if managed, err := f.host.Managed(); managed || err != nil {
		t.Fatalf("native managed=%v err=%v", managed, err)
	}
}

func TestHealthyRepairDoesNotMutate(t *testing.T) {
	f := newFixture(t)
	if err := f.host.Repair(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call, "apt-get ") || strings.HasPrefix(call, "loginctl ") || strings.HasPrefix(call, "systemctl start") || strings.HasPrefix(call, "systemctl reset") {
			t.Fatalf("healthy repair mutated: %s", call)
		}
	}
}

func TestRepairMakesUserSessionPersistentEvenWhileActive(t *testing.T) {
	f := newFixture(t)
	for attempt := 0; attempt < 2; attempt++ {
		if err := os.Remove(f.host.path("/var/lib/systemd/linger/ubuntu")); err != nil {
			t.Fatal(err)
		}
		before := len(f.calls)
		var persistenceMissing bool
		for _, check := range f.host.Inspect(t.Context()) {
			if check.Code == "wsl_user_linger_missing" {
				persistenceMissing = true
			}
		}
		if !persistenceMissing {
			t.Fatal("active user session concealed missing boot persistence")
		}
		for _, call := range f.calls[before:] {
			if strings.HasPrefix(call, "loginctl ") || strings.HasPrefix(call, "systemctl start") {
				t.Fatalf("inspection mutated session state: %s", call)
			}
		}
		before = len(f.calls)
		if err := f.host.Repair(t.Context()); err != nil {
			t.Fatal(err)
		}
		calls := strings.Join(f.calls[before:], "\n")
		if strings.Count(calls, "loginctl enable-linger ubuntu") != 1 || strings.Contains(calls, "apt-get") {
			t.Fatalf("missing linger required one targeted repair: %s", calls)
		}
		// Simulate losing all login sessions, then restarting the user manager
		// during a later boot. The persistent setting survives both inspections.
		f.unhealthy["user@1000.service"] = true
		if healthy(t, f) {
			t.Fatal("inactive user manager reported healthy despite persistence")
		}
		if err := f.host.Repair(t.Context()); err != nil {
			t.Fatal(err)
		}
		if !healthy(t, f) {
			t.Fatal("repair did not restore persistent user session")
		}
	}
}

func TestLingerRepairFailureDoesNotStartServices(t *testing.T) {
	for _, failure := range []string{"command", "missing-marker", "symlink"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			marker := f.host.path("/var/lib/systemd/linger/ubuntu")
			if err := os.Remove(marker); err != nil {
				t.Fatal(err)
			}
			if failure == "symlink" {
				if err := os.Symlink("/dev/null", marker); err != nil {
					t.Fatal(err)
				}
			}
			f.unhealthy["user@1000.service"] = true
			original := f.host.Run
			f.host.Run = func(ctx context.Context, name string, args ...string) (string, error) {
				if filepath.Base(name) == "loginctl" {
					f.calls = append(f.calls, "loginctl "+strings.Join(args, " "))
					if failure == "command" {
						return "", errors.New("logind unavailable")
					}
					return "", nil
				}
				return original(ctx, name, args...)
			}
			if healthy(t, f) || f.host.Repair(t.Context()) == nil {
				t.Fatal("failed persistence accepted")
			}
			for _, call := range f.calls {
				if strings.HasPrefix(call, "systemctl start") || strings.HasPrefix(call, "systemctl reset") || (failure == "symlink" && strings.HasPrefix(call, "loginctl ")) {
					t.Fatalf("failed linger repair changed service or marker state: %s", call)
				}
			}
		})
	}
}

func TestRepairRestoresLogindBeforeEnablingUserPersistence(t *testing.T) {
	f := newFixture(t)
	if err := os.Remove(f.host.path("/var/lib/systemd/linger/ubuntu")); err != nil {
		t.Fatal(err)
	}
	f.unhealthy["dbus.service"] = true
	f.unhealthy["systemd-logind.service"] = true
	f.unhealthy["user@1000.service"] = true
	original := f.host.Run
	f.host.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		if filepath.Base(name) == "loginctl" && (f.unhealthy["dbus.service"] || f.unhealthy["systemd-logind.service"]) {
			t.Fatal("user persistence attempted before logind and its bus were ready")
		}
		return original(ctx, name, args...)
	}
	if err := f.host.Repair(t.Context()); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(f.calls, "\n")
	if strings.Index(calls, "loginctl enable-linger ubuntu") > strings.Index(calls, "systemctl start user@1000.service") || !healthy(t, f) {
		t.Fatalf("user manager did not start after persistence was configured: %s", calls)
	}
}

func TestUserPersistenceRepairRequiresManagedWSL(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(f.host.path("/proc/sys/kernel/osrelease"), []byte("6.18-generic"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.host.path("/var/lib/systemd/linger/ubuntu")); err != nil {
		t.Fatal(err)
	}
	if err := f.host.Repair(t.Context()); err == nil || len(f.calls) != 0 {
		t.Fatal("native Linux host received WSL session policy")
	}
}

func TestRepeatedRepairRestoresMissingPrerequisitesAndPairsPAM(t *testing.T) {
	f := newFixture(t)
	// The host may have received a newer systemd security update.
	f.versions["systemd"] = "255.4-1ubuntu8.99"
	for attempt := 0; attempt < 2; attempt++ {
		f.versions["libpam-systemd"] = ""
		f.versions["kmod"] = ""
		for _, item := range requirements() {
			if item.Package == "libpam-systemd" || item.Package == "kmod" {
				if err := os.Remove(f.host.path(item.Path)); err != nil {
					t.Fatal(err)
				}
			}
		}
		f.unhealthy["user@1000.service"] = true
		f.unhealthy["kmod-static-nodes.service"] = true
		if healthy(t, f) {
			t.Fatal("missing boot dependencies reported healthy")
		}
		before := len(f.calls)
		if err := f.host.Repair(t.Context()); err != nil {
			t.Fatal(err)
		}
		calls := strings.Join(f.calls[before:], "\n")
		if !strings.Contains(calls, "libpam-systemd=255.4-1ubuntu8.99") || !strings.Contains(calls, "--no-remove --no-upgrade --reinstall") {
			t.Fatalf("unsafe package repair: %s", calls)
		}
		if !healthy(t, f) {
			t.Fatal("repair did not restore healthy boot")
		}
	}
}

func TestInspectionDoesNotRepairAndRepairPreservesOtherFailures(t *testing.T) {
	f := newFixture(t)
	f.failed = "foreign.service loaded failed failed Unrelated service"
	if healthy(t, f) {
		t.Fatal("failed unit reported healthy")
	}
	if err := f.host.Repair(t.Context()); err == nil {
		t.Fatal("repair concealed unrelated failed unit")
	}
	for _, call := range f.calls {
		if strings.Contains(call, "reset-failed") || strings.HasPrefix(call, "apt-get") {
			t.Fatalf("unrelated failure changed OS: %s", call)
		}
	}
}

func TestPackageFailureStopsBeforeServiceChanges(t *testing.T) {
	f := newFixture(t)
	f.versions["kmod"] = ""
	f.unhealthy["kmod-static-nodes.service"] = true
	f.aptError = true
	if err := f.host.Repair(t.Context()); err == nil {
		t.Fatal("failed apt transaction accepted")
	}
	for _, call := range f.calls {
		if strings.Contains(call, "reset-failed") || strings.Contains(call, "systemctl start") {
			t.Fatalf("service state changed after apt failure: %s", call)
		}
	}
}

func TestOfflineContractRejectsEveryMissingFileAndExecutableBit(t *testing.T) {
	for _, item := range requirements() {
		t.Run(item.Path, func(t *testing.T) {
			f := newFixture(t)
			if err := os.Remove(f.host.path(item.Path)); err != nil {
				t.Fatal(err)
			}
			if missing := f.host.MissingFiles(); len(missing) != 1 || missing[0] != item.Path {
				t.Fatalf("missing=%v", missing)
			}
			if item.Kind == "executable" {
				f.write(t, item.Path, "file")
				if len(f.host.MissingFiles()) != 1 {
					t.Fatal("non-executable program accepted")
				}
			}
		})
	}
}

func TestSkippedKmodUnitIsHealthy(t *testing.T) {
	f := newFixture(t)
	original := f.host.Run
	f.host.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		if filepath.Base(name) == "systemctl" && args[0] == "show" && args[1] == "kmod-static-nodes.service" {
			return "LoadState=loaded\nActiveState=inactive\nResult=success\nConditionResult=no", nil
		}
		return original(ctx, name, args...)
	}
	if !healthy(t, f) {
		t.Fatal("kernel with no static device nodes rejected")
	}
}

func TestEmbeddedContractIsConsistent(t *testing.T) {
	versions := map[string]string{}
	for _, item := range requirements() {
		if item.Package == "" || item.Version == "" || !filepath.IsAbs(item.Path) || (item.Kind != "file" && item.Kind != "executable") {
			t.Fatalf("invalid requirement: %#v", item)
		}
		if previous := versions[item.Package]; previous != "" && previous != item.Version {
			t.Fatalf("conflicting versions for %s", item.Package)
		}
		versions[item.Package] = item.Version
	}
	for _, item := range requirements() {
		if item.Peer != "-" && versions[item.Peer] != item.Version {
			t.Fatalf("invalid version peer: %#v", item)
		}
	}
}

func TestOfflineInitSymlinkUsesImageFiles(t *testing.T) {
	f := newFixture(t)
	init := f.host.path("/usr/sbin/init")
	if err := os.Remove(init); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/usr/lib/systemd/systemd", init); err != nil {
		t.Fatal(err)
	}
	if missing := f.host.MissingFiles(); len(missing) != 0 {
		t.Fatalf("valid image symlink rejected: %v", missing)
	}
	if err := os.Remove(f.host.path("/usr/lib/systemd/systemd")); err != nil {
		t.Fatal(err)
	}
	missing := f.host.MissingFiles()
	if len(missing) != 2 {
		t.Fatalf("image borrowed host init target: %v", missing)
	}
}

func TestRepairStartsUnloadedUserInstanceWithoutResettingIt(t *testing.T) {
	f := newFixture(t)
	f.unhealthy["user@1000.service"] = true
	original := f.host.Run
	f.host.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		if filepath.Base(name) == "systemctl" && len(args) > 1 && args[1] == "user@1000.service" && f.unhealthy[args[1]] {
			switch args[0] {
			case "show":
				if args[len(args)-1] == "--value" {
					return "inactive", nil
				}
				return "LoadState=loaded\nActiveState=inactive\nResult=success\nConditionResult=yes", nil
			case "reset-failed":
				t.Fatal("reset attempted on unloaded user instance")
			}
		}
		return original(ctx, name, args...)
	}
	if err := f.host.Repair(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !healthy(t, f) {
		t.Fatal("unloaded user instance was not started")
	}
}
