//go:build linux

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
	"loki/internal/admin"
	"loki/internal/config"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/daemon"
	"loki/internal/execution"
	"loki/internal/fault"
	hostpolicy "loki/internal/host/policy"
	"loki/internal/platform/safeio"
)

type applicationClientLayout struct {
	Socket string `json:"socket"`
	UID    uint32 `json:"uid"`
}

// runApplicationAdministration uses only the installed private administrative
// socket. Files are preserved; stdin/private files never become argv or JSON.
func runApplicationAdministration(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "application administration requires the host administrator")
		return 1
	}
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: loki secret app revision|stage|set|env-create|env-remove|bundle-set RESOURCE [OPTIONS]")
		return 2
	}
	if args[0] == "resource" && args[1] == "prepare" {
		return prepareApplicationResource(args[2:], stdout, stderr)
	}
	actions := map[string]string{"revision": "revision", "stage": "stage", "set": "set_secret", "env-create": "env_create", "env-remove": "env_remove", "bundle-set": "bundle_set"}
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

func prepareApplicationResource(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("secret app resource prepare", flag.ContinueOnError)
	flags.SetOutput(stderr)
	catalog := flags.String("catalog", "", "current administrator-owned resource catalog")
	changePath := flags.String("change", "", "typed set/remove/disable resource change")
	configuration := flags.String("config", "/etc/loki-go/runtime.toml", "effective runtime configuration")
	contractPath := flags.String("execution-contract", "/etc/loki-go/execution-contract.json", "installed execution contract")
	output := flags.String("output", "", "new immutable plan file")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *catalog == "" || *changePath == "" || *output == "" {
		return 2
	}
	var current struct {
		Resources []controlpolicy.ApplicationResource `json:"resources"`
	}
	var change hostpolicy.ApplicationChange
	if daemon.ReadJSON(*catalog, &current) != nil || daemon.ReadJSON(*changePath, &change) != nil {
		fmt.Fprintln(stderr, "resource input metadata is unavailable")
		return 1
	}
	c, err := config.Load(*configuration)
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
	raw, err = json.Marshal(plan)
	if err != nil || safeio.PublishPrivate(*output, raw, false) != nil {
		fmt.Fprintln(stderr, "resource plan could not be published")
		return 1
	}
	if json.NewEncoder(stdout).Encode(map[string]any{"expected_policy": plan.ExpectedPolicy, "policy": plan.Policy, "changes": plan.Changes}) != nil {
		return 1
	}
	return 0
}
