//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/config"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/daemon"
	"loki/internal/host/lifecycle"
	hostpolicy "loki/internal/host/policy"
	"loki/internal/platform/safeio"
	"loki/modules/execution/jobs"
	"loki/modules/execution/jobs/remote"
)

// This finite installer bridge operates on public release receipts and the
// existing host/Job journals. It never copies a vault or opens an app key.
func runNativeSwitch(args []string, stdout, stderr io.Writer) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "native release maintenance requires the host administrator")
		return 1
	}
	if len(args) == 0 {
		return 2
	}
	flags := flag.NewFlagSet("host native-switch", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "/", "native installation root")
	target := flags.String("target", "", "verified installed release name")
	operation := flags.String("operation-id", "", "original operation UUID")
	interrupt := flags.Bool("interrupt-active-jobs", false, "explicitly interrupt owned Jobs")
	restored := flags.Bool("restored", false, "original release passed recovery health")
	applicationPlan := flags.String("application-plan", "", "prepared public resource policy for this switch")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || !filepath.IsAbs(*root) || filepath.Clean(*root) != *root ||
		(args[0] != "begin" && args[0] != "finish" && args[0] != "verify" && args[0] != "catalog") || (args[0] == "begin") != (*target != "") ||
		(args[0] != "begin") != (*operation != "") || args[0] == "begin" && *restored || args[0] != "begin" && (*interrupt || *applicationPlan != "") {
		return 2
	}
	rootPath := func(p string) string { return filepath.Join(*root, p) }
	state := rootPath("var/lib/loki-go/native-lifecycle")
	if _, err := lifecycle.EnsureFileStore(state); err != nil {
		fmt.Fprintln(stderr, "native host journal is unavailable")
		return 1
	}
	lock, err := lifecycle.AcquireOperationLock(state)
	if err != nil {
		fmt.Fprintln(stderr, "native host journal is owned or unavailable")
		return 1
	}
	defer lock.Close()
	journal, err := lifecycle.OpenOperationJournal(state, lock, lifecycle.OperationJournalOptions{})
	if err != nil {
		fmt.Fprintln(stderr, "native host journal is invalid")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	channel := nativeMaintenanceChannel{root: *root}
	if args[0] == "begin" {
		current, e := nativeCurrentRelease(*root)
		intent := lifecycle.NativeSwitchIntent{SourceRelease: current, TargetRelease: *target, Interrupt: *interrupt}
		if e == nil && current != "" {
			intent.SourceReceipt, e = nativeReleaseReceipt(*root, current)
		}
		if e == nil {
			intent.TargetReceipt, e = nativeReleaseReceipt(*root, *target)
		}
		if e == nil && *applicationPlan != "" {
			intent.Application, e = prepareNativeApplicationChange(*root, journal, *applicationPlan)
		}
		if e != nil || !intent.Valid() {
			fmt.Fprintln(stderr, "native release receipt or current selection is invalid")
			return 1
		}
		r, e := journal.BeginNativeSwitch(intent, time.Now())
		if e == nil && r.State.Terminal() {
			e = channel.end(ctx, r)
			if e == nil {
				e = journal.AcknowledgeNativeRelease(r.ID)
			}
			if e == nil {
				r, e = journal.BeginNativeSwitch(intent, time.Now())
			}
		}
		if e != nil {
			fmt.Fprintln(stderr, "resume the original native release operation before starting another")
			return 1
		}
		id, _ := lifecycle.MaintenanceOperationID(r.ID)
		if e = channel.begin(ctx, r); e != nil {
			// A definite refusal with a fresh proof that this operation was never
			// admitted may be closed without changing the release. Uncertain IPC
			// retains both the immutable choice and the original operation.
			if channel.notAdmitted(ctx, id) {
				if _, closeErr := journal.CompleteNativeSwitch(r.ID, true, time.Now()); closeErr == nil {
					_ = journal.AcknowledgeNativeRelease(r.ID)
				}
			}
			fmt.Fprintln(stderr, "native release refused: Jobs are busy or cleanup remains unresolved")
			return 1
		}
		source := r.NativeSwitch.SourceRelease
		if source == "" {
			source = "-"
		}
		fmt.Fprintln(stdout, id, source)
		return 0
	}
	id := strings.ReplaceAll(*operation, "-", "")
	if canonical, e := lifecycle.MaintenanceOperationID(id); e != nil || canonical != *operation {
		return 2
	}
	items, err := journal.List()
	if err != nil {
		return 1
	}
	for _, r := range items {
		if r.ID != id || r.NativeSwitch == nil {
			continue
		}
		if args[0] == "catalog" {
			if r.State.Terminal() || r.NativeSwitch.Application == nil {
				fmt.Fprintln(stderr, "no active native application policy change")
				return 1
			}
			if e := channel.begin(ctx, r); e != nil {
				fmt.Fprintln(stderr, "native application policy remains fenced by unresolved cleanup")
				return 1
			}
			resources := r.NativeSwitch.Application.Plan.Resources
			if *restored {
				resources = r.NativeSwitch.Application.Before
			}
			if resources == nil {
				resources = []controlpolicy.ApplicationResource{}
			}
			path := rootPath("etc/loki-go/application-resources.json")
			st, e := os.Lstat(path)
			if e != nil || !st.Mode().IsRegular() {
				return 1
			}
			stat, ok := st.Sys().(*syscall.Stat_t)
			if !ok || stat.Uid != 0 || st.Mode().Perm()&0022 != 0 {
				return 1
			}
			raw, _ := json.Marshal(struct {
				Resources []controlpolicy.ApplicationResource `json:"resources"`
			}{resources})
			if safeio.PublishPrivate(path, raw, true) != nil || os.Chown(path, 0, int(stat.Gid)) != nil || os.Chmod(path, st.Mode().Perm()) != nil {
				fmt.Fprintln(stderr, "native application catalog publication remains incomplete")
				return 1
			}
			return 0
		}
		selected, e := nativeCurrentRelease(*root)
		want := r.NativeSwitch.TargetRelease
		wantReceipt := r.NativeSwitch.TargetReceipt
		if *restored {
			want = r.NativeSwitch.SourceRelease
			wantReceipt = r.NativeSwitch.SourceReceipt
		}
		var receipt string
		if e == nil && selected != "" {
			receipt, e = nativeReleaseReceipt(*root, selected)
		}
		if e != nil || selected != want || receipt != wantReceipt {
			fmt.Fprintln(stderr, "native release selection does not match its health checkpoint")
			return 1
		}
		if change := r.NativeSwitch.Application; change != nil {
			var layout appvalues.BrokerLayout
			wantPolicy := change.Plan.Policy
			if *restored {
				wantPolicy = change.Plan.ExpectedPolicy
			}
			if daemon.ReadJSON(rootPath("run/loki-go/secrets/layout.json"), &layout) != nil || layout.PolicySHA256 != wantPolicy {
				fmt.Fprintln(stderr, "native role policy does not match its health checkpoint")
				return 1
			}
		}
		if args[0] == "verify" {
			return 0
		}
		r, e = journal.CompleteNativeSwitch(id, *restored, time.Now())
		if e == nil {
			e = channel.end(ctx, r)
		}
		if e == nil {
			e = journal.AcknowledgeNativeRelease(id)
		}
		if e != nil {
			fmt.Fprintln(stderr, "native release checkpoint or fence release remains unresolved")
			return 1
		}
		fmt.Fprintln(stdout, "native release maintenance complete")
		return 0
	}
	fmt.Fprintln(stderr, "original native release operation is unavailable")
	return 1
}

func prepareNativeApplicationChange(root string, journal *lifecycle.OperationJournal, path string) (*lifecycle.NativeApplicationChange, error) {
	var plan hostpolicy.ApplicationPlan
	if err := daemon.ReadJSON(path, &plan); err != nil {
		return nil, err
	}
	current, err := hostpolicy.LoadApplications(filepath.Join(root, "etc/loki-go/application-resources.json"))
	if err != nil {
		return nil, err
	}
	before := current
	items, err := journal.List()
	if err != nil {
		return nil, err
	}
	for _, r := range items {
		if r.NativeSwitch != nil && r.NativeSwitch.Application != nil && (!r.State.Terminal() || !r.Maintenance.Released) {
			original := r.NativeSwitch.Application
			if !reflect.DeepEqual(plan, original.Plan) || !reflect.DeepEqual(current, original.Before) && !reflect.DeepEqual(current, original.Plan.Resources) {
				return nil, errors.New("resume the original native application plan")
			}
			before = original.Before
		}
	}
	c, err := config.Load(filepath.Join(root, "etc/loki-go/config.toml"))
	if err != nil {
		return nil, err
	}
	contract, err := loadExecutionContract(filepath.Join(root, "usr/share/doc/loki/execution-contract.json"))
	if err != nil {
		return nil, err
	}
	if _, err := hostpolicy.ValidateApplicationPlan(c, contract, before, plan); err != nil {
		return nil, err
	}
	return &lifecycle.NativeApplicationChange{Before: before, Plan: plan}, nil
}

func nativeCurrentRelease(root string) (string, error) {
	link, err := os.Readlink(filepath.Join(root, "opt/loki-go/current"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil || !strings.HasPrefix(link, "releases/") || filepath.Clean(link) != link || strings.Contains(strings.TrimPrefix(link, "releases/"), "/") {
		return "", errors.New("unmanaged native release link")
	}
	return strings.TrimPrefix(link, "releases/"), nil
}

func nativeReleaseReceipt(root, name string) (string, error) {
	if !((lifecycle.NativeSwitchIntent{TargetRelease: name, TargetReceipt: "sha256:" + strings.Repeat("a", 64)}).Valid()) {
		return "", errors.New("invalid release name")
	}
	base := filepath.Join(root, "opt/loki-go/releases", name)
	raw, err := readNativePublicFile(filepath.Join(base, "SHA256SUMS"), 1<<20)
	if err != nil {
		return "", err
	}
	// Verify the selected executable against the retained install receipt. The
	// shell verified all immutable artifact files before publishing this release.
	program, err := readNativePublicFile(filepath.Join(base, "opt/loki/bin/loki"), 128<<20)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(program)
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "./") == "opt/loki/bin/loki" {
			if found || fields[0] != hex.EncodeToString(sum[:]) {
				return "", errors.New("native executable receipt mismatch")
			}
			found = true
		}
	}
	if !found {
		return "", errors.New("native executable receipt missing")
	}
	sum = sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func readNativePublicFile(path string, max int64) ([]byte, error) {
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "native-release-metadata")
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0022 != 0 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || st.Size > max {
		return nil, errors.New("invalid native release metadata")
	}
	return io.ReadAll(io.LimitReader(f, max+1))
}

type nativeMaintenanceChannel struct{ root string }

func (n nativeMaintenanceChannel) client() (*remote.HostMaintenance, bool, error) {
	path := filepath.Join(n.root, "run/loki-go/launcher/control.sock")
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil || st.Mode()&os.ModeSocket == 0 {
		return nil, false, errors.New("invalid native launcher channel")
	}
	c, err := remote.NewHostMaintenance(path)
	return c, true, err
}

func (n nativeMaintenanceChannel) offline() (*jobs.Journal, error) {
	var layout hostLauncherLayout
	if daemon.ReadJSON(filepath.Join(n.root, "etc/loki-go/launcher.json"), &layout) != nil || layout.StateDirectory != "/var/lib/loki-go/launcher" {
		return nil, errors.New("native launcher layout unavailable")
	}
	return jobs.OpenJournal(filepath.Join(n.root, "var/lib/loki-go/launcher/journal"), jobs.JournalLimits{MaxRecords: layout.MaxJobs, MaxRecordBytes: jobs.JournalRecordBudget(layout.MaxOutputBytes), MaxOutputBytes: layout.MaxOutputBytes, Retention: time.Duration(layout.ResultRetentionSeconds) * time.Second})
}

func (n nativeMaintenanceChannel) begin(ctx context.Context, r lifecycle.OperationRecord) error {
	id, _ := lifecycle.MaintenanceOperationID(r.ID)
	c, live, err := n.client()
	if err != nil {
		return err
	}
	if live {
		_, err = c.Begin(ctx, id, r.Maintenance.Interrupt)
		return err
	}
	if r.NativeSwitch.SourceRelease == "" {
		if _, err := os.Lstat(filepath.Join(n.root, "var/lib/loki-go/launcher/journal")); errors.Is(err, os.ErrNotExist) {
			return nil // First launcher inherits this fence before admission.
		}
	}
	j, err := n.offline()
	if err != nil {
		return err
	}
	defer j.Close()
	// No live owner exists to reconcile OCI identities. Even explicit
	// interruption cannot bypass unresolved cleanup in an offline journal.
	items, err := j.List()
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.State != jobs.StateTerminal || item.Result == nil || item.Result.Cleanup != jobs.CleanupComplete && item.Result.Cleanup != jobs.CleanupNotRequired {
			return jobs.ErrJobsBusy
		}
	}
	_, err = j.BeginMaintenance(id, r.Maintenance.Interrupt)
	return err
}

func (n nativeMaintenanceChannel) notAdmitted(ctx context.Context, id string) bool {
	c, live, err := n.client()
	if err != nil || !live {
		return false
	}
	s, err := c.Status(ctx)
	return err == nil && (s == nil || s.OperationID != id)
}

func (n nativeMaintenanceChannel) end(ctx context.Context, r lifecycle.OperationRecord) error {
	id, _ := lifecycle.MaintenanceOperationID(r.ID)
	c, live, err := n.client()
	if err != nil {
		return err
	}
	if live {
		_, err = c.End(ctx, id)
		return err
	}
	j, err := n.offline()
	if err != nil {
		return err
	}
	defer j.Close()
	_, err = j.EndMaintenance(id)
	return err
}
