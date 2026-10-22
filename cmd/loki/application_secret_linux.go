//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/sys/unix"
	"loki/internal/admin"
	"loki/internal/config"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/daemon"
	"loki/internal/execution"
	"loki/internal/fault"
	"loki/internal/host/lifecycle"
	hostpolicy "loki/internal/host/policy"
	"loki/internal/management"
	"loki/internal/platform/safeio"
)

type applicationClientLayout struct {
	Socket string `json:"socket"`
	UID    uint32 `json:"uid"`
}

// runApplicationAdministration uses only the installed private administrative
// socket. Files are preserved; stdin/private files never become argv or JSON.
func runApplicationAdministration(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) >= 2 && args[0] == "resource" {
		switch args[1] {
		case "prepare":
			return prepareApplicationResource(args[2:], stdout, stderr)
		case "apply":
			return applyApplicationResource(args[2:], stdout, stderr)
		}
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "application administration requires the host administrator")
		return 1
	}
	if len(args) > 0 && args[0] == "maintenance" {
		return runApplicationMaintenance(args[1:], stdout, stderr)
	}
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: loki secret app revision|stage|set|env-create|env-remove|bundle-set|backup-export RESOURCE [OPTIONS]; maintenance begin|end|status [OPTIONS]")
		return 2
	}
	actions := map[string]string{"revision": "revision", "stage": "stage", "set": "set_secret", "env-create": "env_create", "env-remove": "env_remove", "bundle-set": "bundle_set", "backup-export": "backup_export"}
	action, ok := actions[args[0]]
	if !ok {
		fmt.Fprintln(stderr, "unknown application administration action")
		return 2
	}
	q := appvalues.AdminRequest{Action: action, Resource: args[1]}
	flags := flag.NewFlagSet("secret app", flag.ContinueOnError)
	flags.SetOutput(stderr)
	layoutPath := flags.String("layout", "/etc/loki-go/secrets-admin.json", "installed administrator channel layout")
	flags.StringVar(&q.Environment, "env", "", "resource environment; omission selects common")
	flags.StringVar(&q.Revision, "revision", "", "expected opaque edit revision")
	flags.StringVar(&q.RequestID, "request-id", "", "original operation UUID")
	flags.StringVar(&q.Recipient, "recipient", "", "operator X25519 public recipient for a logical app export")
	flags.StringVar(&q.Principal, "principal", "", "principal allowed to consume this stage")
	flags.StringVar(&q.Name, "name", "", "resource secret name")
	flags.BoolVar(&q.Replace, "replace", false, "explicitly replace an existing secret layer")
	variables := flags.String("variables", "", "comma-separated explicit public names for staging")
	members := flags.String("members", "", "comma-separated bundle members")
	file := flags.String("file", "", "private input file, preserved after use")
	fromStdin := flags.Bool("stdin", false, "read bounded private input from stdin")
	if flags.Parse(args[2:]) != nil || flags.NArg() != 0 {
		return 2
	}
	if *variables != "" {
		q.Variables = strings.Split(*variables, ",")
	}
	if *members != "" {
		q.Members = strings.Split(*members, ",")
	}
	needsBody := action == "stage" || action == "set_secret"
	if needsBody && (*file != "") == *fromStdin || !needsBody && (*file != "" || *fromStdin) {
		fmt.Fprintln(stderr, "choose exactly one private input source for stage/set")
		return 2
	}
	var layout applicationClientLayout
	if daemon.ReadJSON(*layoutPath, &layout) != nil || layout.UID == 0 {
		fmt.Fprintln(stderr, "installed application administration layout is unavailable")
		return 1
	}
	var body []byte
	if needsBody {
		reader := stdin
		if *file != "" {
			fd, err := unix.Openat2(unix.AT_FDCWD, *file, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
			if err != nil {
				fmt.Fprintln(stderr, "private input is unavailable")
				return 1
			}
			f := os.NewFile(uintptr(fd), "private-input")
			defer f.Close()
			var st unix.Stat_t
			if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
				fmt.Fprintln(stderr, "private input must be an owned mode-0600 regular file")
				return 1
			}
			reader = f
		}
		maximum := 500000
		if action == "set_secret" {
			maximum = 65536
		}
		var err error
		body, err = io.ReadAll(io.LimitReader(reader, int64(maximum)+1))
		defer clear(body)
		if err != nil || len(body) > maximum || action == "stage" && len(body) == 0 {
			fmt.Fprintln(stderr, "private input exceeds its bound or is unavailable")
			return 1
		}
		q.Empty = action == "set_secret" && len(body) == 0
	}
	out, err := (admin.ApplicationClient{Socket: layout.Socket, ExpectedUID: layout.UID}).Call(context.Background(), q, body)
	if err != nil {
		fmt.Fprintln(stderr, "loki:", fault.Public(err))
		return 1
	}
	var metadata map[string]any
	if json.Unmarshal(out, &metadata) != nil || metadata == nil {
		fmt.Fprintln(stderr, "invalid application metadata response")
		return 1
	}
	if json.NewEncoder(stdout).Encode(metadata) != nil {
		return 1
	}
	return 0
}

func runApplicationMaintenance(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "status" && args[0] != "begin" && args[0] != "end" {
		return 2
	}
	flags := flag.NewFlagSet("secret app maintenance", flag.ContinueOnError)
	flags.SetOutput(stderr)
	layoutPath := flags.String("layout", "/etc/loki-go/secrets-admin.json", "installed private administrator layout")
	operation := flags.String("operation-id", "", "original canonical host operation UUID")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || args[0] == "status" && *operation != "" || args[0] != "status" && !controlpolicy.ApplicationUUID.MatchString(*operation) {
		return 2
	}
	var layout applicationClientLayout
	if daemon.ReadJSON(*layoutPath, &layout) != nil || layout.UID == 0 {
		fmt.Fprintln(stderr, "installed application administration layout is unavailable")
		return 1
	}
	result, err := (admin.ApplicationClient{Socket: layout.Socket, ExpectedUID: layout.UID}).Call(context.Background(), appvalues.AdminRequest{Action: "maintenance_" + args[0], RequestID: *operation}, nil)
	if err != nil {
		fmt.Fprintln(stderr, "loki:", fault.Public(err))
		return 1
	}
	var metadata map[string]any
	if json.Unmarshal(result, &metadata) != nil || metadata == nil || json.NewEncoder(stdout).Encode(metadata) != nil {
		return 1
	}
	return 0
}

func prepareApplicationResource(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("secret app resource prepare", flag.ContinueOnError)
	flags.SetOutput(stderr)
	catalog := flags.String("catalog", "", "current administrator-owned resource catalog")
	changePath := flags.String("change", "", "typed set/remove/disable resource change")
	configuration := flags.String("config", "/etc/loki-go/config.toml", "effective runtime configuration")
	githubConfiguration := flags.String("github-config", "", "deployment-provided public GitHub TOML configuration")
	contractPath := flags.String("execution-contract", "/usr/share/doc/loki/execution-contract.json", "installed execution contract")
	output := flags.String("output", "", "new immutable plan file")
	mode := flags.String("mode", "native", "native, compose or full deployment")
	stateRoot := flags.String("state-root", "", "managed-full host state for preparation")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *changePath == "" || *output == "" ||
		(*mode != "native" && *mode != "compose" && *mode != "full") || *mode != "full" && *catalog == "" || *mode == "full" && *stateRoot == "" {
		return 2
	}
	if *mode == "full" {
		var change hostpolicy.ApplicationChange
		if daemon.ReadJSON(*changePath, &change) != nil {
			fmt.Fprintln(stderr, "resource change is unavailable")
			return 1
		}
		plan, err := (management.Store{Root: *stateRoot}).PrepareApplicationResources(change)
		if err != nil {
			fmt.Fprintln(stderr, "resource plan could not be prepared:", fault.Public(err))
			return 1
		}
		return publishApplicationPlan(plan, *output, stdout, stderr)
	}
	var current struct {
		Resources []controlpolicy.ApplicationResource `json:"resources"`
	}
	var change hostpolicy.ApplicationChange
	if daemon.ReadJSON(*catalog, &current) != nil || daemon.ReadJSON(*changePath, &change) != nil {
		fmt.Fprintln(stderr, "resource input metadata is unavailable")
		return 1
	}
	c, err := config.LoadWithGitHub(*configuration, *githubConfiguration)
	if err != nil {
		fmt.Fprintln(stderr, "runtime configuration is unavailable")
		return 1
	}
	var contract execution.Contract
	raw, err := os.ReadFile(*contractPath)
	if err == nil {
		contract, err = execution.Load(raw)
	}
	if err != nil {
		fmt.Fprintln(stderr, "execution contract is unavailable")
		return 1
	}
	plan, err := hostpolicy.PrepareApplicationChange(c, contract, current.Resources, change)
	if err != nil {
		fmt.Fprintln(stderr, "resource change violates the host policy contract")
		return 1
	}
	return publishApplicationPlan(plan, *output, stdout, stderr)
}

func publishApplicationPlan(plan hostpolicy.ApplicationPlan, output string, stdout, stderr io.Writer) int {
	raw, err := json.Marshal(plan)
	if err != nil || len(raw) > 1<<20 || safeio.PublishPrivate(output, raw, false) != nil {
		fmt.Fprintln(stderr, "resource plan could not be published")
		return 1
	}
	if json.NewEncoder(stdout).Encode(map[string]any{"expected_policy": plan.ExpectedPolicy, "policy": plan.Policy, "changes": plan.Changes}) != nil {
		return 1
	}
	return 0
}

func applyApplicationResource(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("secret app resource apply", flag.ContinueOnError)
	flags.SetOutput(stderr)
	stateRoot := flags.String("state-root", "", "installed Compose host lifecycle root")
	planPath := flags.String("plan", "", "prepared immutable resource plan")
	configuration := flags.String("config", "", "same public runtime configuration used to prepare the plan")
	github := flags.String("github-config", "", "deployment public GitHub configuration")
	contractPath := flags.String("execution-contract", "", "installed execution contract")
	interrupt := flags.Bool("interrupt-active-jobs", false, "explicitly interrupt and verify cleanup of owned Jobs")
	mode := flags.String("mode", "compose", "native, compose or full deployment")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *planPath == "" || *mode != "native" && *stateRoot == "" ||
		(*mode != "native" && *mode != "compose" && *mode != "full") || *mode == "compose" && (*configuration == "" || *contractPath == "") {
		return 2
	}
	var plan hostpolicy.ApplicationPlan
	if daemon.ReadJSON(*planPath, &plan) != nil {
		fmt.Fprintln(stderr, "prepared resource plan is unavailable")
		return 1
	}
	if *mode == "native" {
		if os.Geteuid() != 0 {
			fmt.Fprintln(stderr, "native resource policy requires the host administrator")
			return 1
		}
		args := []string{}
		if *interrupt {
			args = append(args, "--interrupt-active-jobs")
		}
		args = append(args, "apply-resources", *planPath)
		cmd := exec.CommandContext(context.Background(), "/usr/local/sbin/loki-go-lifecycle", args...)
		cmd.Stdout, cmd.Stderr = stderr, stderr
		if cmd.Run() != nil {
			fmt.Fprintln(stderr, "native resource policy is not ready; resume its original plan")
			return 1
		}
		return encodeAppliedApplicationPolicy(plan, stdout)
	}
	if *mode == "full" {
		ctx := context.Background()
		if *interrupt {
			ctx = management.WithJobInterruption(ctx)
		}
		store := management.Store{Root: *stateRoot}
		backend, err := management.NewFullBackend(store, stderr)
		if err == nil {
			store.MaintenanceBackend = backend
			err = store.ApplyApplicationResources(ctx, plan)
		}
		if err == nil {
			_, err = store.ReconcileFull(ctx, backend)
		}
		if err != nil {
			fmt.Fprintln(stderr, "resource policy is not ready; inspect the original operation and retry:", fault.Public(err))
			return 1
		}
		return encodeAppliedApplicationPolicy(plan, stdout)
	}
	c, err := config.LoadWithGitHub(*configuration, *github)
	if err != nil {
		fmt.Fprintln(stderr, "runtime configuration is unavailable")
		return 1
	}
	contract, err := loadExecutionContract(*contractPath)
	if err != nil {
		fmt.Fprintln(stderr, "execution contract is unavailable")
		return 1
	}
	store, err := lifecycle.OpenFileStore(*stateRoot)
	if err != nil {
		fmt.Fprintln(stderr, "installed host state is unavailable")
		return 1
	}
	backend, err := newHostComposeBackend(store)
	if err != nil {
		fmt.Fprintln(stderr, "installed deployment transport is unavailable")
		return 1
	}
	backend.ExpectedApplicationPolicy = plan.Policy
	engine := &lifecycle.TransactionEngine{Store: store, Backend: backend}
	manager := lifecycle.Manager{Store: store, Jobs: backend, Maintainer: engine}
	err = manager.UpdateManagedIntegration(context.Background(), "application", func(ctx context.Context, s *lifecycle.FileStore) error {
		// This runs under the existing host lock after a durable admission fence
		// and recovery snapshot. All roles receive the same catalog on restart.
		actual, err := backend.ApplicationPolicy()
		if err != nil || actual != plan.ExpectedPolicy {
			return errors.New("prepared resource plan does not match the installed role policy")
		}
		raw, err := s.ReadManagedIntegrationFile(ctx, lifecycle.ManagedApplicationResourcesFile, false)
		if err != nil {
			return err
		}
		current := struct {
			Resources []controlpolicy.ApplicationResource `json:"resources"`
		}{}
		if len(raw) != 0 && json.Unmarshal(raw, &current) != nil {
			return errors.New("installed resource catalog is invalid")
		}
		next, err := hostpolicy.ValidateApplicationPlan(c, contract, current.Resources, plan)
		if err != nil {
			return err
		}
		current.Resources = next
		raw, err = json.Marshal(current)
		if err == nil {
			_, err = s.WriteManagedIntegrationFile(ctx, lifecycle.ManagedApplicationResourcesFile, raw)
		}
		return err
	}, lifecycle.MutationOptions{InterruptActiveJobs: *interrupt})
	if err != nil {
		fmt.Fprintln(stderr, "resource policy was not committed:", fault.Public(err))
		return 1
	}
	return encodeAppliedApplicationPolicy(plan, stdout)
}

func encodeAppliedApplicationPolicy(plan hostpolicy.ApplicationPlan, stdout io.Writer) int {
	if json.NewEncoder(stdout).Encode(map[string]any{"applied": true, "policy": plan.Policy}) != nil {
		return 1
	}
	return 0
}
