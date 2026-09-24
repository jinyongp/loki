// Package sandbox implements the finite, administrator-constrained OCI workload boundary.
package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	minMemoryBytes    = 64 << 20
	maxMemoryBytes    = 64 << 30
	minTmpfsBytes     = 16 << 20
	maxTmpfsBytes     = 4 << 30
	minPIDs           = 16
	maxPIDs           = 4096
	maxArgs           = 256
	maxArgBytes       = 64 << 10
	maxEnv            = 256
	maxEnvBytes       = 64 << 10
	maxEndpoints      = 8
	maxToolchains     = 8
	maxRunOutputBytes = 64 << 20
)

var (
	digestPattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	imageDigestPattern   = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[0-9]{1,5})?(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*@sha256:[0-9a-f]{64}$`)
	jobIDPattern         = regexp.MustCompile(`^[0-9a-f]{32}$`)
	envNamePattern       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	endpointNamePattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	toolchainNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

type NetworkProfile string

const (
	NetworkNone              NetworkProfile = "none"
	NetworkDependencyInstall NetworkProfile = "dependency-install"
)

func (p NetworkProfile) Valid() bool {
	return p == NetworkNone || p == NetworkDependencyInstall
}

type EndpointSpec struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

type GatewayPolicyOptions struct {
	Image             string
	Binary            string
	ExecutionContract string
	EgressPolicy      string
	ProxyPort         int
	MemoryBytes       int64
	PIDs              int64
	TmpfsBytes        int64
}

type GatewayPolicy struct {
	image             string
	binary            string
	executionContract string
	egressPolicy      string
	proxyPort         int
	memoryBytes       int64
	pids              int64
	tmpfsBytes        int64
}

type PolicyOptions struct {
	GenerationSHA256   string
	Image              string
	Gateway            GatewayPolicyOptions
	Workspace          string
	InputDirectory     string
	ToolchainDirectory string
	UID, GID           uint32
	Environment        []string
	MemoryBytes        int64
	PIDs               int64
	TmpfsBytes         int64
}

type Policy struct {
	generationSHA256   string
	sandboxSHA256      string
	image              string
	gateway            GatewayPolicy
	workspace          string
	inputDirectory     string
	toolchainDirectory string
	uid, gid           uint32
	environment        []string
	memoryBytes        int64
	pids               int64
	tmpfsBytes         int64
}

type ToolchainMount struct {
	Family string
	Source string
}

type WorkloadSpec struct {
	ID             string           `json:"id"`
	PolicySHA256   string           `json:"policy_sha256"`
	CWD            string           `json:"cwd"`
	Argv           []string         `json:"argv"`
	Network        NetworkProfile   `json:"network"`
	Endpoints      []EndpointSpec   `json:"endpoints,omitempty"`
	Toolchains     []ToolchainMount `json:"-"`
	InputPath      string           `json:"input_path,omitempty"`
	MaxOutputBytes int              `json:"max_output_bytes,omitempty"`
}

type Plan struct {
	valid              bool
	name               string
	policySHA256       string
	sandboxSHA256      string
	resource           Resource
	network            NetworkProfile
	endpoints          []EndpointSpec
	toolchains         []ToolchainMount
	gateway            GatewayPolicy
	inputDirectory     string
	toolchainDirectory string
	inputPath          string
	maxOutputBytes     int
	create             dockerCreateRequest
}

type dockerCreateRequest struct {
	Image            string                  `json:"Image"`
	Cmd              []string                `json:"Cmd"`
	Env              []string                `json:"Env"`
	outputBytes      int                     `json:"-"`
	WorkingDir       string                  `json:"WorkingDir"`
	User             string                  `json:"User"`
	NetworkDisabled  bool                    `json:"NetworkDisabled"`
	AttachStdout     bool                    `json:"AttachStdout"`
	AttachStderr     bool                    `json:"AttachStderr"`
	ExposedPorts     map[string]struct{}     `json:"ExposedPorts,omitempty"`
	Labels           map[string]string       `json:"Labels"`
	HostConfig       dockerHostConfig        `json:"HostConfig"`
	NetworkingConfig *dockerNetworkingConfig `json:"NetworkingConfig,omitempty"`
}

type dockerHostConfig struct {
	ReadonlyRootfs  bool                           `json:"ReadonlyRootfs"`
	CapDrop         []string                       `json:"CapDrop"`
	SecurityOpt     []string                       `json:"SecurityOpt"`
	NetworkMode     string                         `json:"NetworkMode"`
	Memory          int64                          `json:"Memory"`
	PidsLimit       int64                          `json:"PidsLimit"`
	Mounts          []dockerMount                  `json:"Mounts"`
	Tmpfs           map[string]string              `json:"Tmpfs"`
	LogConfig       dockerLogConfig                `json:"LogConfig"`
	PortBindings    map[string][]dockerPortBinding `json:"PortBindings,omitempty"`
	PublishAllPorts bool                           `json:"PublishAllPorts"`
	Init            bool                           `json:"Init"`
}

type dockerPortBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type dockerNetworkingConfig struct {
	EndpointsConfig map[string]dockerEndpointSettings `json:"EndpointsConfig"`
}

type dockerEndpointSettings struct {
	Aliases []string `json:"Aliases,omitempty"`
}

type dockerLogConfig struct {
	Type   string            `json:"Type"`
	Config map[string]string `json:"Config"`
}

type dockerMount struct {
	Type        string             `json:"Type"`
	Source      string             `json:"Source"`
	Target      string             `json:"Target"`
	ReadOnly    bool               `json:"ReadOnly"`
	BindOptions *dockerBindOptions `json:"BindOptions,omitempty"`
}

type dockerBindOptions struct {
	Propagation string `json:"Propagation"`
}

func cleanAbsoluteNonRoot(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value &&
		value != string(filepath.Separator) && !strings.ContainsRune(value, 0)
}

func normalizeGateway(options GatewayPolicyOptions) (GatewayPolicy, error) {
	if !imageDigestPattern.MatchString(options.Image) {
		return GatewayPolicy{}, errors.New("sandbox gateway image must be pinned by sha256 digest")
	}
	if !cleanAbsoluteNonRoot(options.Binary) || !cleanAbsoluteNonRoot(options.ExecutionContract) ||
		!cleanAbsoluteNonRoot(options.EgressPolicy) {
		return GatewayPolicy{}, errors.New("sandbox gateway paths must be absolute clean non-root paths")
	}
	if options.ProxyPort < 1024 || options.ProxyPort > 65535 {
		return GatewayPolicy{}, errors.New("sandbox gateway proxy port is outside the supported range")
	}
	if options.MemoryBytes < minMemoryBytes || options.MemoryBytes > maxMemoryBytes {
		return GatewayPolicy{}, errors.New("sandbox gateway memory limit is outside the supported range")
	}
	if options.PIDs < minPIDs || options.PIDs > maxPIDs {
		return GatewayPolicy{}, errors.New("sandbox gateway PID limit is outside the supported range")
	}
	if options.TmpfsBytes < minTmpfsBytes || options.TmpfsBytes > maxTmpfsBytes ||
		options.TmpfsBytes > options.MemoryBytes {
		return GatewayPolicy{}, errors.New("sandbox gateway temporary-storage limit is outside the supported range")
	}
	return GatewayPolicy{
		image: options.Image, binary: options.Binary,
		executionContract: options.ExecutionContract, egressPolicy: options.EgressPolicy,
		proxyPort: options.ProxyPort, memoryBytes: options.MemoryBytes,
		pids: options.PIDs, tmpfsBytes: options.TmpfsBytes,
	}, nil
}

func (g GatewayPolicy) valid() bool {
	return imageDigestPattern.MatchString(g.image) &&
		cleanAbsoluteNonRoot(g.binary) && cleanAbsoluteNonRoot(g.executionContract) &&
		cleanAbsoluteNonRoot(g.egressPolicy) && g.proxyPort >= 1024 && g.proxyPort <= 65535 &&
		g.memoryBytes >= minMemoryBytes && g.memoryBytes <= maxMemoryBytes &&
		g.pids >= minPIDs && g.pids <= maxPIDs &&
		g.tmpfsBytes >= minTmpfsBytes && g.tmpfsBytes <= maxTmpfsBytes &&
		g.tmpfsBytes <= g.memoryBytes
}

func sandboxPolicyFingerprint(
	generationSHA256, image, workspace, inputDirectory, toolchainDirectory string,
	uid, gid uint32,
	environment []string,
	memoryBytes, pids, tmpfsBytes int64,
	gateway GatewayPolicy,
) (string, error) {
	payload := struct {
		GenerationSHA256   string   `json:"generation_sha256"`
		Image              string   `json:"image"`
		Workspace          string   `json:"workspace"`
		InputDirectory     string   `json:"input_directory,omitempty"`
		ToolchainDirectory string   `json:"toolchain_directory,omitempty"`
		UID                uint32   `json:"uid"`
		GID                uint32   `json:"gid"`
		Environment        []string `json:"environment"`
		MemoryBytes        int64    `json:"memory_bytes"`
		PIDs               int64    `json:"pids"`
		TmpfsBytes         int64    `json:"tmpfs_bytes"`
		Gateway            struct {
			Image             string `json:"image"`
			Binary            string `json:"binary"`
			ExecutionContract string `json:"execution_contract"`
			EgressPolicy      string `json:"egress_policy"`
			ProxyPort         int    `json:"proxy_port"`
			MemoryBytes       int64  `json:"memory_bytes"`
			PIDs              int64  `json:"pids"`
			TmpfsBytes        int64  `json:"tmpfs_bytes"`
		} `json:"gateway"`
	}{
		GenerationSHA256: generationSHA256, Image: image, Workspace: workspace, InputDirectory: inputDirectory,
		ToolchainDirectory: toolchainDirectory, UID: uid, GID: gid, Environment: append([]string(nil), environment...),
		MemoryBytes: memoryBytes, PIDs: pids, TmpfsBytes: tmpfsBytes,
	}
	payload.Gateway.Image = gateway.image
	payload.Gateway.Binary = gateway.binary
	payload.Gateway.ExecutionContract = gateway.executionContract
	payload.Gateway.EgressPolicy = gateway.egressPolicy
	payload.Gateway.ProxyPort = gateway.proxyPort
	payload.Gateway.MemoryBytes = gateway.memoryBytes
	payload.Gateway.PIDs = gateway.pids
	payload.Gateway.TmpfsBytes = gateway.tmpfsBytes
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func NewPolicy(options PolicyOptions) (Policy, error) {
	if !digestPattern.MatchString(options.GenerationSHA256) {
		return Policy{}, errors.New("sandbox policy requires a valid effective-policy digest")
	}
	if !imageDigestPattern.MatchString(options.Image) {
		return Policy{}, errors.New("sandbox image must be pinned by sha256 digest")
	}
	if !cleanAbsoluteNonRoot(options.Workspace) {
		return Policy{}, errors.New("sandbox workspace must be an absolute clean non-root path")
	}
	if options.InputDirectory != "" && !cleanAbsoluteNonRoot(options.InputDirectory) {
		return Policy{}, errors.New("sandbox input directory must be an absolute clean non-root path")
	}
	if options.ToolchainDirectory != "" && !cleanAbsoluteNonRoot(options.ToolchainDirectory) {
		return Policy{}, errors.New("sandbox toolchain directory must be an absolute clean non-root path")
	}
	if options.UID == 0 || options.GID == 0 {
		return Policy{}, errors.New("sandbox workload identity must be unprivileged")
	}
	if options.MemoryBytes < minMemoryBytes || options.MemoryBytes > maxMemoryBytes {
		return Policy{}, errors.New("sandbox memory limit is outside the supported range")
	}
	if options.PIDs < minPIDs || options.PIDs > maxPIDs {
		return Policy{}, errors.New("sandbox PID limit is outside the supported range")
	}
	if options.TmpfsBytes < minTmpfsBytes || options.TmpfsBytes > maxTmpfsBytes || options.TmpfsBytes > options.MemoryBytes {
		return Policy{}, errors.New("sandbox temporary-storage limit is outside the supported range")
	}
	environment, err := normalizeEnvironment(options.Environment)
	if err != nil {
		return Policy{}, err
	}
	gateway, err := normalizeGateway(options.Gateway)
	if err != nil {
		return Policy{}, err
	}
	sandboxSHA256, err := sandboxPolicyFingerprint(
		options.GenerationSHA256, options.Image, options.Workspace, options.InputDirectory, options.ToolchainDirectory,
		options.UID, options.GID, environment, options.MemoryBytes, options.PIDs, options.TmpfsBytes, gateway,
	)
	if err != nil {
		return Policy{}, err
	}
	return Policy{
		generationSHA256: options.GenerationSHA256, sandboxSHA256: sandboxSHA256,
		image: options.Image, gateway: gateway, workspace: options.Workspace, inputDirectory: options.InputDirectory,
		toolchainDirectory: options.ToolchainDirectory, uid: options.UID, gid: options.GID, environment: environment,
		memoryBytes: options.MemoryBytes, pids: options.PIDs, tmpfsBytes: options.TmpfsBytes,
	}, nil
}

func (p Policy) Valid() bool {
	if !digestPattern.MatchString(p.generationSHA256) || !digestPattern.MatchString(p.sandboxSHA256) ||
		!imageDigestPattern.MatchString(p.image) || !p.gateway.valid() ||
		!cleanAbsoluteNonRoot(p.workspace) ||
		p.inputDirectory != "" && !cleanAbsoluteNonRoot(p.inputDirectory) ||
		p.toolchainDirectory != "" && !cleanAbsoluteNonRoot(p.toolchainDirectory) ||
		p.uid == 0 || p.gid == 0 || p.memoryBytes < minMemoryBytes || p.memoryBytes > maxMemoryBytes ||
		p.pids < minPIDs || p.pids > maxPIDs || p.tmpfsBytes < minTmpfsBytes || p.tmpfsBytes > maxTmpfsBytes ||
		p.tmpfsBytes > p.memoryBytes {
		return false
	}
	normalized, err := normalizeEnvironment(p.environment)
	if err != nil || len(normalized) != len(p.environment) {
		return false
	}
	for index := range normalized {
		if normalized[index] != p.environment[index] {
			return false
		}
	}
	fingerprint, err := sandboxPolicyFingerprint(
		p.generationSHA256, p.image, p.workspace, p.inputDirectory, p.toolchainDirectory, p.uid, p.gid, p.environment,
		p.memoryBytes, p.pids, p.tmpfsBytes, p.gateway,
	)
	return err == nil && fingerprint == p.sandboxSHA256
}

func (p Policy) InputDirectory() string {
	if !p.Valid() {
		return ""
	}
	return p.inputDirectory
}

func normalizeEnvironment(values []string) ([]string, error) {
	if len(values) > maxEnv {
		return nil, errors.New("sandbox environment has too many entries")
	}
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	total := 0
	for _, value := range values {
		if strings.ContainsRune(value, 0) || len(value) > 8192 {
			return nil, errors.New("sandbox environment entry is invalid")
		}
		name, _, ok := strings.Cut(value, "=")
		if !ok || !envNamePattern.MatchString(name) {
			return nil, errors.New("sandbox environment entry is invalid")
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, errors.New("sandbox environment contains a duplicate name")
		}
		seen[name] = struct{}{}
		total += len(value)
		if total > maxEnvBytes {
			return nil, errors.New("sandbox environment exceeds its size limit")
		}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func normalizeNetworkProfile(value NetworkProfile) (NetworkProfile, error) {
	if value == "" {
		return NetworkNone, nil
	}
	if !value.Valid() {
		return "", errors.New("sandbox workload network profile is invalid")
	}
	return value, nil
}

func normalizeEndpointSpecs(values []EndpointSpec, reservedPort int) ([]EndpointSpec, error) {
	if len(values) > maxEndpoints {
		return nil, errors.New("sandbox endpoint count exceeds its limit")
	}
	result := append([]EndpointSpec(nil), values...)
	for _, endpoint := range result {
		if !endpointNamePattern.MatchString(endpoint.Name) ||
			endpoint.Port < 1024 || endpoint.Port > 65535 || endpoint.Port == reservedPort {
			return nil, errors.New("sandbox endpoint is invalid")
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].Port < result[j].Port
		}
		return result[i].Name < result[j].Name
	})
	ports := map[int]bool{}
	for index, endpoint := range result {
		if index > 0 && result[index-1].Name == endpoint.Name || ports[endpoint.Port] {
			return nil, errors.New("sandbox endpoints must use unique names and ports")
		}
		ports[endpoint.Port] = true
	}
	return result, nil
}

func normalizeToolchainMounts(values []ToolchainMount, directory string) ([]ToolchainMount, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > maxToolchains || !cleanAbsoluteNonRoot(directory) {
		return nil, errors.New("sandbox toolchain mounts require a trusted toolchain directory")
	}
	generations := filepath.Join(directory, "generations")
	result := append([]ToolchainMount(nil), values...)
	for _, value := range result {
		if !toolchainNamePattern.MatchString(value.Family) || !cleanAbsoluteNonRoot(value.Source) {
			return nil, errors.New("sandbox toolchain mount is invalid")
		}
		relative, err := filepath.Rel(generations, value.Source)
		if err != nil || !filepath.IsLocal(relative) {
			return nil, errors.New("sandbox toolchain mount escapes the trusted store")
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		if len(parts) != 2 || !digestPattern.MatchString(parts[0]) || parts[1] != "root" {
			return nil, errors.New("sandbox toolchain mount does not identify an immutable generation root")
		}
		info, err := os.Lstat(value.Source)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0222 != 0 {
			return nil, errors.New("sandbox toolchain generation root is missing or mutable")
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Family < result[j].Family })
	for index := 1; index < len(result); index++ {
		if result[index-1].Family == result[index].Family {
			return nil, errors.New("sandbox toolchain families must be unique")
		}
	}
	return result, nil
}

func (p Policy) Plan(spec WorkloadSpec) (Plan, error) {
	if !p.Valid() || spec.PolicySHA256 != p.generationSHA256 {
		return Plan{}, errors.New("workload policy generation does not match the launcher")
	}
	if !jobIDPattern.MatchString(spec.ID) {
		return Plan{}, errors.New("workload ID is invalid")
	}
	cwd, err := workloadCWD(spec.CWD)
	if err != nil {
		return Plan{}, err
	}
	argv, err := workloadArgv(spec.Argv)
	if err != nil {
		return Plan{}, err
	}
	network, err := normalizeNetworkProfile(spec.Network)
	if err != nil {
		return Plan{}, err
	}
	endpoints, err := normalizeEndpointSpecs(spec.Endpoints, p.gateway.proxyPort)
	if err != nil {
		return Plan{}, err
	}
	toolchains, err := normalizeToolchainMounts(spec.Toolchains, p.toolchainDirectory)
	if err != nil {
		return Plan{}, err
	}
	if spec.InputPath != "" {
		expected := filepath.Join(p.inputDirectory, spec.ID+".stdin")
		if p.inputDirectory == "" || !cleanAbsoluteNonRoot(spec.InputPath) || spec.InputPath != expected {
			return Plan{}, errors.New("sandbox workload input path is not allowed")
		}
	}
	if spec.MaxOutputBytes < 0 || spec.MaxOutputBytes > maxRunOutputBytes {
		return Plan{}, errors.New("sandbox workload output limit is outside the supported range")
	}
	resource, err := NewResource(spec.ID, p.generationSHA256, p.sandboxSHA256)
	if err != nil {
		return Plan{}, err
	}
	environment := append([]string(nil), p.environment...)
	tmpfs := fmt.Sprintf("rw,noexec,nosuid,nodev,size=%d,uid=%d,gid=%d,mode=0700", p.tmpfsBytes, p.uid, p.gid)
	needsGateway := network == NetworkDependencyInstall || len(endpoints) > 0
	create := dockerCreateRequest{
		Image:           p.image,
		Cmd:             argv,
		Env:             environment,
		outputBytes:     spec.MaxOutputBytes,
		WorkingDir:      cwd,
		User:            strconv.FormatUint(uint64(p.uid), 10) + ":" + strconv.FormatUint(uint64(p.gid), 10),
		NetworkDisabled: !needsGateway,
		AttachStdout:    true,
		AttachStderr:    true,
		Labels:          resource.labels(),
		HostConfig: dockerHostConfig{
			ReadonlyRootfs: true,
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges:true"},
			NetworkMode:    "none",
			Memory:         p.memoryBytes,
			PidsLimit:      p.pids,
			Mounts: []dockerMount{{
				Type:     "bind",
				Source:   p.workspace,
				Target:   "/workspace",
				ReadOnly: false,
				BindOptions: &dockerBindOptions{
					Propagation: "rprivate",
				},
			}},
			Tmpfs: map[string]string{"/tmp": tmpfs},
			Init:  true,
		},
	}
	for _, toolchain := range toolchains {
		create.HostConfig.Mounts = append(create.HostConfig.Mounts, dockerMount{
			Type: "bind", Source: toolchain.Source, Target: path.Join("/opt/loki/managed", toolchain.Family), ReadOnly: true,
			BindOptions: &dockerBindOptions{Propagation: "rprivate"},
		})
	}
	if spec.InputPath != "" {
		create.Cmd = append([]string{"/bin/sh", "-c", `exec "$@" < /loki-run-input`, "--"}, create.Cmd...)
		create.HostConfig.Mounts = append(create.HostConfig.Mounts, dockerMount{
			Type: "bind", Source: spec.InputPath, Target: "/loki-run-input", ReadOnly: true,
			BindOptions: &dockerBindOptions{Propagation: "rprivate"},
		})
	}
	return Plan{
		valid: true, name: resource.Name(), policySHA256: p.generationSHA256,
		sandboxSHA256: p.sandboxSHA256, resource: resource, network: network,
		endpoints: endpoints, toolchains: toolchains, gateway: p.gateway,
		inputDirectory: p.inputDirectory, toolchainDirectory: p.toolchainDirectory, inputPath: spec.InputPath,
		maxOutputBytes: spec.MaxOutputBytes, create: create,
	}, nil
}

func workloadCWD(value string) (string, error) {
	if value == "" || strings.ContainsRune(value, 0) || len(value) > 4096 || path.IsAbs(value) {
		return "", errors.New("workload working directory is invalid")
	}
	clean := path.Clean(value)
	if clean != value || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("workload working directory is invalid")
	}
	if clean == "." {
		return "/workspace", nil
	}
	return path.Join("/workspace", clean), nil
}

func workloadArgv(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > maxArgs || !path.IsAbs(values[0]) || path.Clean(values[0]) != values[0] {
		return nil, errors.New("workload command is invalid")
	}
	result := make([]string, len(values))
	total := 0
	for index, value := range values {
		if strings.ContainsRune(value, 0) || len(value) > 4096 || index == 0 && value == "/" {
			return nil, errors.New("workload command is invalid")
		}
		total += len(value)
		if total > maxArgBytes {
			return nil, errors.New("workload command exceeds its size limit")
		}
		result[index] = value
	}
	return result, nil
}

func (p Plan) Valid() bool {
	if !p.valid || !p.resource.Valid() || p.name != p.resource.Name() ||
		p.policySHA256 != p.resource.PolicySHA256() ||
		p.sandboxSHA256 != p.resource.SandboxSHA256() ||
		!digestPattern.MatchString(p.sandboxSHA256) || !p.network.Valid() ||
		p.maxOutputBytes < 0 || p.maxOutputBytes > maxRunOutputBytes ||
		!p.gateway.valid() || p.create.Image == "" || !p.resource.owns(p.create.Labels) {
		return false
	}
	if p.inputPath != "" {
		if p.inputDirectory == "" || !cleanAbsoluteNonRoot(p.inputPath) ||
			p.inputPath != filepath.Join(p.inputDirectory, p.resource.JobID()+".stdin") {
			return false
		}
	}
	normalized, err := normalizeEndpointSpecs(p.endpoints, p.gateway.proxyPort)
	if err != nil || len(normalized) != len(p.endpoints) {
		return false
	}
	for index := range normalized {
		if normalized[index] != p.endpoints[index] {
			return false
		}
	}
	toolchains, err := normalizeToolchainMounts(p.toolchains, p.toolchainDirectory)
	if err != nil || len(toolchains) != len(p.toolchains) {
		return false
	}
	for index := range toolchains {
		if toolchains[index] != p.toolchains[index] {
			return false
		}
	}
	return true
}

func (p Plan) Name() string {
	if !p.Valid() {
		return ""
	}
	return p.name
}

func (p Plan) PolicySHA256() string {
	if !p.Valid() {
		return ""
	}
	return p.policySHA256
}

func (p Plan) SandboxSHA256() string {
	if !p.Valid() {
		return ""
	}
	return p.sandboxSHA256
}

func (p Plan) NetworkProfile() NetworkProfile {
	if !p.Valid() {
		return ""
	}
	return p.network
}

func (p Plan) Endpoints() []EndpointSpec {
	if !p.Valid() {
		return nil
	}
	return append([]EndpointSpec(nil), p.endpoints...)
}

func (p Plan) NeedsGateway() bool {
	return p.Valid() && (p.network == NetworkDependencyInstall || len(p.endpoints) > 0)
}

func (p Plan) NeedsOutboundNetwork() bool {
	return p.NeedsGateway()
}

func (p Plan) GatewayName() string {
	if !p.NeedsGateway() {
		return ""
	}
	return p.resource.GatewayName()
}

func (p Plan) InternalNetworkName() string {
	if !p.NeedsGateway() {
		return ""
	}
	return p.resource.InternalNetworkName()
}

func (p Plan) OutboundNetworkName() string {
	if !p.NeedsOutboundNetwork() {
		return ""
	}
	return p.resource.OutboundNetworkName()
}

func (p Plan) Resource() Resource {
	if !p.Valid() {
		return Resource{}
	}
	return p.resource
}
