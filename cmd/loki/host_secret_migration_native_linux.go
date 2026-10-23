//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"

	"loki/internal/admin"
	"loki/internal/config"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	hostapplication "loki/internal/host/application"
	"loki/internal/host/lifecycle"
	"loki/internal/host/migration"
	hostpolicy "loki/internal/host/policy"
	"loki/internal/platform/safeio"
)

var errNativeMigration = errors.New("native secret migration requires recovery through its original host operation")

func nativeMigrationOptions() (hostapplication.PrepareOptions, error) {
	var options hostapplication.PrepareOptions
	identity, err := hostapplication.ReadIdentity("/etc/loki-go/secrets-deployment.json")
	if err != nil {
		return options, err
	}
	if _, err := user.LookupId(strconv.FormatUint(uint64(identity.UID), 10)); err != nil {
		return options, errors.New("installed native broker account is unavailable")
	}
	for _, id := range []uint32{identity.GID, identity.MetadataGID} {
		if _, err := user.LookupGroupId(strconv.FormatUint(uint64(id), 10)); err != nil {
			return options, errors.New("installed native broker groups are unavailable")
		}
	}
	c, err := config.Load("/etc/loki-go/config.toml")
	if err != nil {
		return options, err
	}
	contract, err := loadExecutionContract("/usr/share/doc/loki/execution-contract.json")
	if err != nil {
		return options, err
	}
	resources, err := hostpolicy.LoadApplications("/etc/loki-go/application-resources.json")
	if err != nil {
		return options, err
	}
	options = hostapplication.PrepareOptions{UID: identity.UID, GID: identity.GID, AgentUID: identity.AgentUID, MetadataGID: identity.MetadataGID, IdentityFile: "/etc/loki-go/secrets-deployment.json", StateRoot: "/var/lib/loki-go/secrets", ServiceStateRoot: "/var/lib/loki-go/secrets", RuntimeRoot: "/run/loki-go/secrets", Binary: "/opt/loki/bin/devtools", Principal: "agent", Configuration: c, Contract: contract, Resources: resources}
	return options, hostapplication.ValidateMetadata(options)
}

func runHostNativeSecretMigration(args []string, stdout, stderr io.Writer) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "native migration requires the host administrator")
		return 1
	}
	if len(args) == 0 {
		return 2
	}
	action := args[0]
	if action != "prepare" && action != "apply" && action != "status" && action != "recover" && action != "rollback" {
		return 2
	}
	flags := flag.NewFlagSet("host secret-migration native "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	mapping := flags.String("mapping", "", "protected public mappings/resources JSON; prepare only")
	plan := flags.String("plan", "", "exact prepared native intent; apply only")
	recipient := flags.String("recipient", "", "operator X25519 public recipient; prepare only")
	backup := flags.String("backup", "", "unused private encrypted source archive; prepare only")
	interrupt := flags.Bool("interrupt-active-jobs", false, "freeze the explicit interruption choice; prepare only")
	approve := flags.Bool("approve", false, "approve the exact prepared plan or original pre-cutover rollback")
	protection := flags.String("protection", "", "explicit candidate init or unlock; apply/recover only")
	private := flags.Bool("stdin", false, "receive bounded protection bytes privately")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || *protection != "" && *protection != "init" && *protection != "unlock" || (*protection != "") != *private {
		return 2
	}
	if action == "prepare" {
		if *mapping == "" || *recipient == "" || *backup == "" || *plan != "" || *approve || *private {
			return 2
		}
	} else if *mapping != "" || *recipient != "" || *backup != "" || *interrupt || (action == "apply") != (*plan != "") || (action == "apply" || action == "rollback") != *approve || action != "apply" && action != "recover" && *private {
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	state := "/var/lib/loki-go/native-lifecycle"
	if _, err := lifecycle.EnsureFileStore(state); err != nil {
		fmt.Fprintln(stderr, errNativeMigration)
		return 1
	}
	lock, err := lifecycle.AcquireOperationLock(state)
	if err != nil {
		fmt.Fprintln(stderr, errNativeMigration)
		return 1
	}
	defer lock.Close()
	j, err := lifecycle.OpenOperationJournal(state, lock, lifecycle.OperationJournalOptions{})
	if err != nil {
		fmt.Fprintln(stderr, errNativeMigration)
		return 1
	}
	if action == "status" {
		items, err := j.List()
		if err == nil {
			for n := len(items) - 1; n >= 0; n-- {
				if items[n].SecretMigration != nil && items[n].SecretMigration.Intent.Mode == "native" {
					r := items[n]
					return encodeNativeMigration(stdout, migration.NativeProgress{Operation: lifecycle.MigrationOperationUUID(r.ID), State: r.State, Phase: r.Phase, Generation: r.SecretMigration.Intent.CandidateGeneration, Released: r.Maintenance.Released, Rollback: r.SecretMigration.RollbackRequested})
				}
			}
		}
		fmt.Fprintln(stderr, "no recorded native secret migration exists")
		return 1
	}
	// The installed immutable release remains selected throughout value
	// migration; an installer cannot substitute another binary during recovery.
	release, err := nativeCurrentRelease("/")
	if err == nil && release != "" {
		_, err = nativeReleaseReceipt("/", release)
	} else {
		err = errNativeMigration
	}
	if err != nil {
		fmt.Fprintln(stderr, errNativeMigration)
		return 1
	}
	options, err := nativeMigrationOptions()
	if err != nil {
		fmt.Fprintln(stderr, errNativeMigration)
		return 1
	}
	if action == "prepare" {
		var input struct {
			Mappings  []appvalues.MigrationProfileMapping `json:"mappings"`
			Resources []controlpolicy.ApplicationResource `json:"resources"`
		}
		if err := readSecretMigrationDocument(*mapping, &input); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		before := options.Resources
		options.Resources = input.Resources
		intent, err := migration.PrepareNative(ctx, migration.NativePrepareOptions{Application: options, BeforeResources: before, Mappings: input.Mappings, SourceRoot: "/var/lib/loki-go/runtime", Recipient: *recipient, BackupPath: *backup, Interrupt: *interrupt})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return encodeNativeMigration(stdout, intent)
	}
	var record lifecycle.OperationRecord
	if action == "apply" {
		var intent lifecycle.SecretMigrationIntent
		if readSecretMigrationDocument(*plan, &intent) != nil || !intent.Valid() || intent.Mode != "native" || intent.SourceRoot != "/var/lib/loki-go/runtime" || intent.IdentityFile != options.IdentityFile || intent.StateRoot != options.StateRoot || intent.ServiceStateRoot != options.ServiceStateRoot || intent.Binary != options.Binary || intent.RuntimeRoot != "/run/loki-go/secret-migration-"+intent.CandidateGeneration {
			fmt.Fprintln(stderr, "exact prepared native migration plan is unavailable or invalid")
			return 1
		}
		items, listErr := j.List()
		if listErr != nil {
			fmt.Fprintln(stderr, errNativeMigration)
			return 1
		}
		for _, old := range items {
			if old.SecretMigration != nil && reflect.DeepEqual(old.SecretMigration.Intent, intent) {
				record = old
				break
			}
		}
		if record.ID == "" {
			before, readErr := hostapplication.ReadIdentity(intent.IdentityFile)
			policy, policyErr := hostapplication.PolicyDigest(options)
			if readErr != nil || policyErr != nil || before != intent.Before || policy != intent.ExpectedPolicy {
				fmt.Fprintln(stderr, "prepared native identity or policy changed before admission")
				return 1
			}
			record, err = j.BeginSecretMigration(intent, time.Now())
		}
	} else {
		var found bool
		record, found, err = j.Active()
		if err == nil && (!found || record.SecretMigration == nil || record.SecretMigration.Intent.Mode != "native") {
			err = errNativeMigration
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, errNativeMigration)
		return 1
	}
	if record.Maintenance.Released {
		return encodeNativeMigration(stdout, migration.NativeProgress{Operation: lifecycle.MigrationOperationUUID(record.ID), State: record.State, Phase: record.Phase, Generation: record.SecretMigration.Intent.CandidateGeneration, Released: true, Rollback: record.SecretMigration.RollbackRequested})
	}
	owner := migration.SourceOwner{Journal: j, ID: record.ID}
	if action == "rollback" {
		if err := owner.RequestNativeRollback(); err != nil {
			fmt.Fprintln(stderr, "rollback requires the original pre-cutover selector")
			return 1
		}
	}
	var passphrase []byte
	if *private {
		passphrase, err = io.ReadAll(io.LimitReader(os.Stdin, 65537))
		defer clear(passphrase)
		if err != nil || len(passphrase) == 0 || len(passphrase) > 65536 {
			fmt.Fprintln(stderr, "provide bounded explicit protection bytes on stdin")
			return 1
		}
	}
	result, err := owner.RunNative(ctx, options, nativeMigrationServices{options: options, stderr: stderr}, "/opt/loki/bin/loki-secret-broker", options.Binary, *protection, passphrase)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return encodeNativeMigration(stdout, result)
}

func encodeNativeMigration(w io.Writer, value any) int {
	if json.NewEncoder(w).Encode(value) != nil {
		return 1
	}
	return 0
}

type nativeMigrationServices struct {
	options hostapplication.PrepareOptions
	stderr  io.Writer
}

func nativeMigrationControl(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/bin/systemctl", args...)
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent"}
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = io.Discard
	err := command.Run()
	if output.Len() > 128<<10 {
		return nil, errNativeMigration
	}
	return output.Bytes(), err
}

var nativeMigrationServiceNames = []string{"loki-go.target", "loki-go-mcp.service", "loki-go-executor.service", "loki-go-launcher.service", "loki-go-runtime.service", "loki-go-secrets.service", "loki-go-secrets-prepare.service", "loki-go-browser.service", "loki-go-browser-proxy.service", "loki-go-signing-agent.service", "loki-go-port-guard.service", "loki-go-egress-proxy.service"}

func (n nativeMigrationServices) Freeze(ctx context.Context, r lifecycle.OperationRecord) error {
	if err := (nativeMaintenanceChannel{root: "/"}).begin(ctx, r); err != nil {
		return err
	}
	if info, err := os.Lstat("/run/loki-go/secrets/admin/control.sock"); err == nil && info.Mode()&os.ModeSocket != 0 {
		if err := (admin.ApplicationClient{Socket: "/run/loki-go/secrets/admin/control.sock", ExpectedUID: r.SecretMigration.Intent.Before.UID}).FenceApplicationWrites(ctx, lifecycle.MigrationOperationUUID(r.ID), true); err != nil {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	var loaded []string
	for _, unit := range nativeMigrationServiceNames {
		body, err := nativeMigrationControl(ctx, "show", unit, "--property=LoadState", "--value")
		if strings.TrimSpace(string(body)) == "not-found" {
			continue
		}
		if err != nil || strings.TrimSpace(string(body)) != "loaded" {
			return errNativeMigration
		}
		loaded = append(loaded, unit)
	}
	if len(loaded) == 0 {
		return errNativeMigration
	}
	if _, err := nativeMigrationControl(ctx, append([]string{"stop"}, loaded...)...); err != nil {
		return errNativeMigration
	}
	for _, unit := range loaded {
		body, err := nativeMigrationControl(ctx, "show", unit, "--property=ActiveState,MainPID,ControlPID")
		if err != nil || !strings.Contains(string(body), "ActiveState=inactive\n") || strings.Contains(string(body), "MainPID=") && !strings.Contains(string(body), "MainPID=0\n") || strings.Contains(string(body), "ControlPID=") && !strings.Contains(string(body), "ControlPID=0\n") {
			return errNativeMigration
		}
	}
	return nil
}

func nativeMigrationPrepareUnit(r lifecycle.OperationRecord) []byte {
	i := r.SecretMigration.Intent
	command := "/opt/loki/bin/loki host prepare-secrets --uid " + strconv.FormatUint(uint64(i.Before.UID), 10) + " --gid " + strconv.FormatUint(uint64(i.Before.GID), 10) + " --agent-uid " + strconv.FormatUint(uint64(i.Before.AgentUID), 10) + " --metadata-gid " + strconv.FormatUint(uint64(i.Before.MetadataGID), 10) + " --unit-directory= --overlay-directory= --initial-maintenance-operation " + lifecycle.MigrationOperationUUID(r.ID)
	return []byte("# Original LOKI secret migration " + r.ID + "\n[Service]\nExecStart=\nExecStart=" + command + "\n")
}

func (n nativeMigrationServices) PrepareServing(ctx context.Context, r lifecycle.OperationRecord, restored bool) error {
	i := r.SecretMigration.Intent
	actual, err := hostapplication.ReadIdentity(i.IdentityFile)
	want := i.Before
	resources := i.BeforeResources
	if !restored {
		want.Version, want.ActiveGeneration, want.PolicySHA256 = 2, i.CandidateGeneration, i.Policy
		resources = i.Plan.Resources
	}
	if err != nil || actual != want || hostapplication.RequireExistingGeneration(i.StateRoot, want) != nil {
		return errNativeMigration
	}
	current, err := hostpolicy.LoadApplications("/etc/loki-go/application-resources.json")
	beforeResources, beforeErr := controlpolicy.CanonicalApplications(i.BeforeResources, n.options.Contract.Environment)
	if err == nil {
		current, err = controlpolicy.CanonicalApplications(current, n.options.Contract.Environment)
	}
	if err != nil || beforeErr != nil || !reflect.DeepEqual(current, beforeResources) && !reflect.DeepEqual(current, i.Plan.Resources) {
		return errNativeMigration
	}
	if resources == nil {
		resources = []controlpolicy.ApplicationResource{}
	}
	body, _ := json.Marshal(struct {
		Resources []controlpolicy.ApplicationResource `json:"resources"`
	}{resources})
	if err := safeio.PublishPrivateOwned("/etc/loki-go/application-resources.json", body, true, 0, int(want.MetadataGID)); err != nil {
		return err
	}
	if err := os.Chmod("/etc/loki-go/application-resources.json", 0640); err != nil {
		return err
	}
	args := []string{"--uid", strconv.FormatUint(uint64(want.UID), 10), "--gid", strconv.FormatUint(uint64(want.GID), 10), "--agent-uid", strconv.FormatUint(uint64(want.AgentUID), 10), "--metadata-gid", strconv.FormatUint(uint64(want.MetadataGID), 10), "--maintenance-operation", lifecycle.MigrationOperationUUID(r.ID), "--initial-maintenance-operation", lifecycle.MigrationOperationUUID(r.ID)}
	if i.Interrupt {
		args = append(args, "--interrupt-jobs")
	}
	if runHostPrepareSecrets(args, io.Discard, n.stderr) != 0 {
		return errNativeMigration
	}
	path := "/etc/systemd/system/loki-go-secrets-prepare.service.d/71-secret-migration-prepare.conf"
	if existing, err := readNativePublicFile(path, 16384); err == nil {
		if !bytes.Equal(existing, nativeMigrationPrepareUnit(r)) {
			return errNativeMigration
		}
	} else if !os.IsNotExist(err) {
		return err
	} else if err := safeio.PublishPrivate(path, nativeMigrationPrepareUnit(r), false); err != nil {
		return err
	}
	_, err = nativeMigrationControl(ctx, "daemon-reload")
	return err
}

func (n nativeMigrationServices) StartServing(ctx context.Context, _ lifecycle.OperationRecord) error {
	_, err := nativeMigrationControl(ctx, "start", "loki-go.target")
	return err
}

func (n nativeMigrationServices) Health(ctx context.Context, r lifecycle.OperationRecord) error {
	for _, unit := range []string{"loki-go-mcp.service", "loki-go-runtime.service", "loki-go-secrets.service", "loki-go-launcher.service", "loki-go-executor.service"} {
		if _, err := nativeMigrationControl(ctx, "is-active", "--quiet", unit); err != nil {
			return errNativeMigration
		}
	}
	uid := strconv.FormatUint(uint64(r.SecretMigration.Intent.Before.UID), 10)
	if runFullProbe([]string{"--role", "application-listeners", "--socket", "/run/loki-go/secrets", "--expected-uid", uid}, n.stderr) != 0 {
		return errNativeMigration
	}
	address := "http://127.0.0.1:" + strconv.Itoa(n.options.Configuration.Port) + "/mcp"
	if runFullProbe([]string{"--role", "mcp", "--address", address, "--token-file", "/etc/loki-go/token"}, n.stderr) != 0 {
		return errNativeMigration
	}
	return nil
}

func (n nativeMigrationServices) Release(ctx context.Context, r lifecycle.OperationRecord) error {
	if err := (admin.ApplicationClient{Socket: "/run/loki-go/secrets/admin/control.sock", ExpectedUID: r.SecretMigration.Intent.Before.UID}).FenceApplicationWrites(ctx, lifecycle.MigrationOperationUUID(r.ID), false); err != nil {
		return err
	}
	if err := (nativeMaintenanceChannel{root: "/"}).end(ctx, r); err != nil {
		return err
	}
	path := "/etc/systemd/system/loki-go-secrets-prepare.service.d/71-secret-migration-prepare.conf"
	body, err := readNativePublicFile(path, 16384)
	if err == nil {
		if !bytes.Equal(body, nativeMigrationPrepareUnit(r)) {
			return errNativeMigration
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
