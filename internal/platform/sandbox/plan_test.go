package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validPolicyOptions() PolicyOptions {
	return PolicyOptions{
		GenerationSHA256: strings.Repeat("a", 64),
		Image:            "registry.example/loki@sha256:" + strings.Repeat("b", 64),
		Gateway: GatewayPolicyOptions{
			Image:             "registry.example/loki-gateway@sha256:" + strings.Repeat("c", 64),
			Binary:            "/opt/loki/bin/loki",
			ExecutionContract: "/usr/share/doc/loki/execution-contract.json",
			EgressPolicy:      "/usr/share/doc/loki/egress-policy.json",
			ProxyPort:         18766,
			MemoryBytes:       128 << 20,
			PIDs:              64,
			TmpfsBytes:        32 << 20,
		},
		Workspace:   "/srv/loki-workspace",
		UID:         10000,
		GID:         10000,
		Environment: []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"},
		MemoryBytes: 512 << 20,
		PIDs:        128,
		TmpfsBytes:  64 << 20,
	}
}

func validWorkloadSpec() WorkloadSpec {
	return WorkloadSpec{
		ID:           strings.Repeat("c", 32),
		PolicySHA256: strings.Repeat("a", 64),
		CWD:          ".",
		Argv:         []string{"/usr/bin/git", "status", "--short"},
	}
}

func TestPolicyValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PolicyOptions)
	}{
		{"digest", func(o *PolicyOptions) { o.GenerationSHA256 = "bad" }},
		{"image-tag", func(o *PolicyOptions) { o.Image = "loki:latest" }},
		{"image-scheme", func(o *PolicyOptions) { o.Image = "https://registry.example/loki@sha256:" + strings.Repeat("b", 64) }},
		{"gateway-image", func(o *PolicyOptions) { o.Gateway.Image = "loki:latest" }},
		{"gateway-binary", func(o *PolicyOptions) { o.Gateway.Binary = "loki" }},
		{"gateway-contract", func(o *PolicyOptions) { o.Gateway.ExecutionContract = "contract.json" }},
		{"gateway-egress-policy", func(o *PolicyOptions) { o.Gateway.EgressPolicy = "policy.json" }},
		{"gateway-port", func(o *PolicyOptions) { o.Gateway.ProxyPort = 80 }},
		{"gateway-memory", func(o *PolicyOptions) { o.Gateway.MemoryBytes = minMemoryBytes - 1 }},
		{"gateway-pids", func(o *PolicyOptions) { o.Gateway.PIDs = minPIDs - 1 }},
		{"gateway-tmpfs", func(o *PolicyOptions) { o.Gateway.TmpfsBytes = minTmpfsBytes - 1 }},
		{"workspace-relative", func(o *PolicyOptions) { o.Workspace = "workspace" }},
		{"workspace-root", func(o *PolicyOptions) { o.Workspace = "/" }},
		{"workspace-dirty", func(o *PolicyOptions) { o.Workspace = "/srv/../workspace" }},
		{"root-uid", func(o *PolicyOptions) { o.UID = 0 }},
		{"root-gid", func(o *PolicyOptions) { o.GID = 0 }},
		{"memory-low", func(o *PolicyOptions) { o.MemoryBytes = minMemoryBytes - 1 }},
		{"memory-high", func(o *PolicyOptions) { o.MemoryBytes = maxMemoryBytes + 1 }},
		{"pids-low", func(o *PolicyOptions) { o.PIDs = minPIDs - 1 }},
		{"pids-high", func(o *PolicyOptions) { o.PIDs = maxPIDs + 1 }},
		{"tmpfs-low", func(o *PolicyOptions) { o.TmpfsBytes = minTmpfsBytes - 1 }},
		{"tmpfs-high", func(o *PolicyOptions) { o.TmpfsBytes = maxTmpfsBytes + 1 }},
		{"tmpfs-over-memory", func(o *PolicyOptions) { o.TmpfsBytes = o.MemoryBytes + 1 }},
		{"env-name", func(o *PolicyOptions) { o.Environment = []string{"1BAD=value"} }},
		{"env-duplicate", func(o *PolicyOptions) { o.Environment = []string{"A=1", "A=2"} }},
		{"env-nul", func(o *PolicyOptions) { o.Environment = []string{"A=x\x00y"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := validPolicyOptions()
			test.mutate(&options)
			if _, err := NewPolicy(options); err == nil {
				t.Fatal("invalid policy was accepted")
			}
		})
	}
	options := validPolicyOptions()
	options.Environment = make([]string, maxEnv+1)
	for index := range options.Environment {
		options.Environment[index] = "V" + strings.Repeat("X", index%5) + "=1"
	}
	if _, err := NewPolicy(options); err == nil {
		t.Fatal("oversized environment count was accepted")
	}
}

func TestWorkloadSpecValidation(t *testing.T) {
	policy, err := NewPolicy(validPolicyOptions())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*WorkloadSpec)
	}{
		{"policy", func(s *WorkloadSpec) { s.PolicySHA256 = strings.Repeat("d", 64) }},
		{"id-short", func(s *WorkloadSpec) { s.ID = "abc" }},
		{"id-upper", func(s *WorkloadSpec) { s.ID = strings.Repeat("C", 32) }},
		{"cwd-empty", func(s *WorkloadSpec) { s.CWD = "" }},
		{"cwd-absolute", func(s *WorkloadSpec) { s.CWD = "/workspace" }},
		{"cwd-traversal", func(s *WorkloadSpec) { s.CWD = "../outside" }},
		{"cwd-unclean", func(s *WorkloadSpec) { s.CWD = "a/../b" }},
		{"argv-empty", func(s *WorkloadSpec) { s.Argv = nil }},
		{"argv-relative", func(s *WorkloadSpec) { s.Argv[0] = "git" }},
		{"argv-root", func(s *WorkloadSpec) { s.Argv[0] = "/" }},
		{"argv-nul", func(s *WorkloadSpec) { s.Argv[1] = "bad\x00arg" }},
		{"network", func(s *WorkloadSpec) { s.Network = NetworkProfile("host") }},
		{"endpoint-name", func(s *WorkloadSpec) { s.Endpoints = []EndpointSpec{{Name: "Web", Port: 5173}} }},
		{"endpoint-port", func(s *WorkloadSpec) { s.Endpoints = []EndpointSpec{{Name: "web", Port: 80}} }},
		{"endpoint-proxy-port", func(s *WorkloadSpec) { s.Endpoints = []EndpointSpec{{Name: "web", Port: 18766}} }},
		{"endpoint-duplicate", func(s *WorkloadSpec) {
			s.Endpoints = []EndpointSpec{{Name: "web", Port: 5173}, {Name: "api", Port: 5173}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := validWorkloadSpec()
			spec.Argv = append([]string(nil), spec.Argv...)
			test.mutate(&spec)
			if _, err := policy.Plan(spec); err == nil {
				t.Fatal("invalid workload was accepted")
			}
		})
	}
	spec := validWorkloadSpec()
	spec.Argv = make([]string, maxArgs+1)
	spec.Argv[0] = "/bin/true"
	for index := 1; index < len(spec.Argv); index++ {
		spec.Argv[index] = "x"
	}
	if _, err := policy.Plan(spec); err == nil {
		t.Fatal("too many arguments were accepted")
	}
}

func TestPlanMountsOnlyImmutableManagedToolchainGenerations(t *testing.T) {
	store := filepath.Join(t.TempDir(), "toolchains")
	generations := filepath.Join(store, "generations")
	if err := os.MkdirAll(generations, 0755); err != nil {
		t.Fatal(err)
	}
	generationRoot := filepath.Join(generations, strings.Repeat("d", 64), "root")
	if err := os.MkdirAll(generationRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(generationRoot, 0555); err != nil {
		t.Fatal(err)
	}
	options := validPolicyOptions()
	options.ToolchainDirectory = store
	policy, err := NewPolicy(options)
	if err != nil {
		t.Fatal(err)
	}
	spec := validWorkloadSpec()
	spec.Toolchains = []ToolchainMount{{Family: "node", Source: generationRoot}}
	plan, err := policy.Plan(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.create.HostConfig.Mounts) != 2 {
		t.Fatalf("mounts = %#v", plan.create.HostConfig.Mounts)
	}
	mount := plan.create.HostConfig.Mounts[1]
	if mount.Source != generationRoot || mount.Target != "/opt/loki/managed/node" || !mount.ReadOnly {
		t.Fatalf("toolchain mount = %#v", mount)
	}

	outside := spec
	outside.Toolchains = []ToolchainMount{{Family: "node", Source: filepath.Join(t.TempDir(), "root")}}
	if _, err = policy.Plan(outside); err == nil {
		t.Fatal("toolchain mount outside trusted store was accepted")
	}
	writableRoot := filepath.Join(store, "generations", strings.Repeat("e", 64), "root")
	if err = os.MkdirAll(writableRoot, 0755); err != nil {
		t.Fatal(err)
	}
	writable := spec
	writable.Toolchains = []ToolchainMount{{Family: "node", Source: writableRoot}}
	if _, err = policy.Plan(writable); err == nil {
		t.Fatal("writable toolchain generation was accepted")
	}
	duplicate := spec
	duplicate.Toolchains = []ToolchainMount{
		{Family: "node", Source: generationRoot},
		{Family: "node", Source: generationRoot},
	}
	if _, err = policy.Plan(duplicate); err == nil {
		t.Fatal("duplicate toolchain family was accepted")
	}
}

func TestPlanAcceptsOnlyPolicyOwnedRunInput(t *testing.T) {
	options := validPolicyOptions()
	options.InputDirectory = "/var/lib/loki/run-inputs"
	policy, err := NewPolicy(options)
	if err != nil {
		t.Fatal(err)
	}
	spec := validWorkloadSpec()
	spec.InputPath = filepath.Join(options.InputDirectory, spec.ID+".stdin")
	spec.MaxOutputBytes = 8 << 20
	plan, err := policy.Plan(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Valid() || len(plan.create.HostConfig.Mounts) != 2 {
		t.Fatalf("input plan = %#v", plan)
	}
	mount := plan.create.HostConfig.Mounts[1]
	if mount.Source != spec.InputPath || mount.Target != "/loki-run-input" || !mount.ReadOnly {
		t.Fatalf("input mount = %#v", mount)
	}
	wantPrefix := []string{"/bin/sh", "-c", `exec "$@" < /loki-run-input`, "--"}
	if len(plan.create.Cmd) < len(wantPrefix)+len(spec.Argv) {
		t.Fatalf("wrapped command = %#v", plan.create.Cmd)
	}
	for index, want := range wantPrefix {
		if plan.create.Cmd[index] != want {
			t.Fatalf("wrapped command[%d] = %q, want %q", index, plan.create.Cmd[index], want)
		}
	}

	for _, path := range []string{"/tmp/input", filepath.Join(options.InputDirectory, strings.Repeat("d", 32)+".stdin")} {
		changed := spec
		changed.InputPath = path
		if _, err = policy.Plan(changed); err == nil {
			t.Fatalf("untrusted input path %q was accepted", path)
		}
	}
	changed := spec
	changed.MaxOutputBytes = maxRunOutputBytes + 1
	if _, err = policy.Plan(changed); err == nil {
		t.Fatal("oversized synchronous output limit was accepted")
	}
}

func TestPlanUsesOnlyFixedSecurityEnvelope(t *testing.T) {
	options := validPolicyOptions()
	options.Environment = []string{"Z=last", "A=first"}
	policy, err := NewPolicy(options)
	if err != nil {
		t.Fatal(err)
	}
	spec := validWorkloadSpec()
	spec.CWD = "repo/subdir"
	plan, err := policy.Plan(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Valid() || plan.Name() != "loki-job-"+spec.ID || plan.PolicySHA256() != options.GenerationSHA256 ||
		len(plan.SandboxSHA256()) != 64 || plan.NetworkProfile() != NetworkNone ||
		plan.NeedsGateway() || plan.NeedsOutboundNetwork() {
		t.Fatalf("plan metadata = %#v", plan)
	}
	create := plan.create
	if create.Image != options.Image || create.WorkingDir != "/workspace/repo/subdir" ||
		create.User != "10000:10000" || !create.NetworkDisabled || !create.AttachStdout || !create.AttachStderr {
		t.Fatalf("create request = %#v", create)
	}
	if got := create.Env; len(got) != 2 || got[0] != "A=first" || got[1] != "Z=last" {
		t.Fatalf("environment = %#v", got)
	}
	if resource := plan.Resource(); !resource.Valid() || !resource.owns(create.Labels) {
		t.Fatalf("resource labels = %#v", create.Labels)
	}
	host := create.HostConfig
	if !host.ReadonlyRootfs || host.NetworkMode != "none" || host.Memory != options.MemoryBytes ||
		host.PidsLimit != options.PIDs || !host.Init {
		t.Fatalf("host config = %#v", host)
	}
	if len(host.CapDrop) != 1 || host.CapDrop[0] != "ALL" ||
		len(host.SecurityOpt) != 1 || host.SecurityOpt[0] != "no-new-privileges:true" {
		t.Fatalf("security envelope = %#v", host)
	}
	if len(host.Mounts) != 1 {
		t.Fatalf("mounts = %#v", host.Mounts)
	}
	mount := host.Mounts[0]
	if mount.Type != "bind" || mount.Source != options.Workspace || mount.Target != "/workspace" || mount.ReadOnly ||
		mount.BindOptions == nil || mount.BindOptions.Propagation != "rprivate" {
		t.Fatalf("workspace mount = %#v", mount)
	}
	tmpfs := host.Tmpfs["/tmp"]
	for _, want := range []string{"noexec", "nosuid", "nodev", "uid=10000", "gid=10000", "mode=0700"} {
		if !strings.Contains(tmpfs, want) {
			t.Fatalf("tmpfs %q missing %q", tmpfs, want)
		}
	}

	raw, err := json.Marshal(create)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	hostDocument := document["HostConfig"].(map[string]any)
	for _, forbidden := range []string{"Privileged", "CapAdd", "Devices", "Binds", "VolumesFrom", "PidMode", "IpcMode", "UTSMode", "CgroupnsMode"} {
		if _, ok := hostDocument[forbidden]; ok {
			t.Fatalf("forbidden Docker field %q present in %s", forbidden, raw)
		}
	}
}

func TestPlanDefinesPolicyBoundNetworkDomain(t *testing.T) {
	options := validPolicyOptions()
	policy, err := NewPolicy(options)
	if err != nil {
		t.Fatal(err)
	}
	spec := validWorkloadSpec()
	spec.Network = NetworkDependencyInstall
	spec.Endpoints = []EndpointSpec{{Name: "web", Port: 5173}, {Name: "api", Port: 3000}}
	plan, err := policy.Plan(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.NeedsGateway() || !plan.NeedsOutboundNetwork() ||
		plan.GatewayName() != "loki-job-gateway-"+spec.ID ||
		plan.InternalNetworkName() != "loki-job-net-"+spec.ID ||
		plan.OutboundNetworkName() != "loki-job-egress-"+spec.ID ||
		plan.create.NetworkDisabled || plan.create.HostConfig.NetworkMode != "none" {
		t.Fatalf("network plan = %#v", plan)
	}
	endpoints := plan.Endpoints()
	if len(endpoints) != 2 || endpoints[0].Name != "api" || endpoints[1].Name != "web" {
		t.Fatalf("endpoints = %#v", endpoints)
	}
	endpoints[0].Name = "mutated"
	if plan.Endpoints()[0].Name != "api" {
		t.Fatal("plan endpoint projection retained caller alias")
	}
	resource := plan.Resource()
	if !resource.owns(plan.create.Labels) ||
		plan.create.Labels[resourceSandboxLabel] != plan.SandboxSHA256() {
		t.Fatalf("workload labels = %#v", plan.create.Labels)
	}
	if !resource.ownsComponent(resource.labelsFor(resourceComponentGateway), resourceComponentGateway) ||
		!resource.ownsComponent(resource.labelsFor(resourceComponentInternalNetwork), resourceComponentInternalNetwork) ||
		!resource.ownsComponent(resource.labelsFor(resourceComponentOutboundNetwork), resourceComponentOutboundNetwork) {
		t.Fatal("subordinate resource labels are not policy-bound")
	}

	changed := options
	changed.Gateway.Image = "registry.example/loki-gateway@sha256:" + strings.Repeat("d", 64)
	changedPolicy, err := NewPolicy(changed)
	if err != nil {
		t.Fatal(err)
	}
	changedPlan, err := changedPolicy.Plan(spec)
	if err != nil {
		t.Fatal(err)
	}
	if changedPlan.PolicySHA256() != plan.PolicySHA256() ||
		changedPlan.SandboxSHA256() == plan.SandboxSHA256() {
		t.Fatalf("private policy fingerprint did not track gateway authority: %q %q", plan.SandboxSHA256(), changedPlan.SandboxSHA256())
	}

	endpointOnly := validWorkloadSpec()
	endpointOnly.Endpoints = []EndpointSpec{{Name: "web", Port: 5173}}
	endpointPlan, err := policy.Plan(endpointOnly)
	if err != nil {
		t.Fatal(err)
	}
	if !endpointPlan.NeedsGateway() || endpointPlan.NeedsOutboundNetwork() ||
		endpointPlan.OutboundNetworkName() != "" {
		t.Fatalf("endpoint-only network plan = %#v", endpointPlan)
	}
}

func TestPolicyAndPlanCopyCallerData(t *testing.T) {
	options := validPolicyOptions()
	options.Environment = []string{"A=one"}
	policy, err := NewPolicy(options)
	if err != nil {
		t.Fatal(err)
	}
	options.Environment[0] = "A=mutated"

	spec := validWorkloadSpec()
	spec.Argv = []string{"/bin/echo", "original"}
	plan, err := policy.Plan(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Argv[1] = "mutated"

	if plan.create.Env[0] != "A=one" || plan.create.Cmd[1] != "original" {
		t.Fatalf("plan retained caller aliases: env=%#v argv=%#v", plan.create.Env, plan.create.Cmd)
	}
	if (Plan{}).Valid() || (Plan{}).Name() != "" || (Plan{}).PolicySHA256() != "" ||
		(Plan{}).SandboxSHA256() != "" || (Plan{}).NeedsGateway() {
		t.Fatal("zero plan became valid")
	}
}
