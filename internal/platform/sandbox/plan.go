// Package sandbox implements the finite, administrator-constrained OCI workload boundary.
package sandbox

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	minMemoryBytes = 64 << 20
	maxMemoryBytes = 64 << 30
	minTmpfsBytes  = 16 << 20
	maxTmpfsBytes  = 4 << 30
	minPIDs        = 16
	maxPIDs        = 4096
	maxArgs        = 256
	maxArgBytes    = 64 << 10
	maxEnv         = 256
	maxEnvBytes    = 64 << 10
)

var (
	digestPattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	imageDigestPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*@sha256:[0-9a-f]{64}$`)
	jobIDPattern       = regexp.MustCompile(`^[0-9a-f]{32}$`)
	envNamePattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type PolicyOptions struct {
	GenerationSHA256 string
	Image            string
	Workspace        string
	UID, GID         uint32
	Environment      []string
	MemoryBytes      int64
	PIDs             int64
	TmpfsBytes       int64
}

type Policy struct {
	generationSHA256 string
	image            string
	workspace        string
	uid, gid         uint32
	environment      []string
	memoryBytes      int64
	pids             int64
	tmpfsBytes       int64
}

type WorkloadSpec struct {
	ID           string   `json:"id"`
	PolicySHA256 string   `json:"policy_sha256"`
	CWD          string   `json:"cwd"`
	Argv         []string `json:"argv"`
}

type Plan struct {
	valid        bool
	name         string
	policySHA256 string
	create       dockerCreateRequest
}

type dockerCreateRequest struct {
	Image           string           `json:"Image"`
	Cmd             []string         `json:"Cmd"`
	Env             []string         `json:"Env"`
	WorkingDir      string           `json:"WorkingDir"`
	User            string           `json:"User"`
	NetworkDisabled bool             `json:"NetworkDisabled"`
	AttachStdout    bool             `json:"AttachStdout"`
	AttachStderr    bool             `json:"AttachStderr"`
	HostConfig      dockerHostConfig `json:"HostConfig"`
}

type dockerHostConfig struct {
	ReadonlyRootfs bool              `json:"ReadonlyRootfs"`
	CapDrop        []string          `json:"CapDrop"`
	SecurityOpt    []string          `json:"SecurityOpt"`
	NetworkMode    string            `json:"NetworkMode"`
	Memory         int64             `json:"Memory"`
	PidsLimit      int64             `json:"PidsLimit"`
	Mounts         []dockerMount     `json:"Mounts"`
	Tmpfs          map[string]string `json:"Tmpfs"`
	Init           bool              `json:"Init"`
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

func NewPolicy(options PolicyOptions) (Policy, error) {
	if !digestPattern.MatchString(options.GenerationSHA256) {
		return Policy{}, errors.New("sandbox policy requires a valid effective-policy digest")
	}
	if !imageDigestPattern.MatchString(options.Image) {
		return Policy{}, errors.New("sandbox image must be pinned by sha256 digest")
	}
	if !filepath.IsAbs(options.Workspace) || filepath.Clean(options.Workspace) != options.Workspace || options.Workspace == string(filepath.Separator) || strings.ContainsRune(options.Workspace, 0) {
		return Policy{}, errors.New("sandbox workspace must be an absolute clean non-root path")
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
	return Policy{
		generationSHA256: options.GenerationSHA256,
		image:            options.Image,
		workspace:        options.Workspace,
		uid:              options.UID,
		gid:              options.GID,
		environment:      environment,
		memoryBytes:      options.MemoryBytes,
		pids:             options.PIDs,
		tmpfsBytes:       options.TmpfsBytes,
	}, nil
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

func (p Policy) Plan(spec WorkloadSpec) (Plan, error) {
	if p.generationSHA256 == "" || !digestPattern.MatchString(p.generationSHA256) || spec.PolicySHA256 != p.generationSHA256 {
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
	environment := append([]string(nil), p.environment...)
	tmpfs := fmt.Sprintf("rw,noexec,nosuid,nodev,size=%d,uid=%d,gid=%d,mode=0700", p.tmpfsBytes, p.uid, p.gid)
	create := dockerCreateRequest{
		Image:           p.image,
		Cmd:             argv,
		Env:             environment,
		WorkingDir:      cwd,
		User:            strconv.FormatUint(uint64(p.uid), 10) + ":" + strconv.FormatUint(uint64(p.gid), 10),
		NetworkDisabled: true,
		AttachStdout:    true,
		AttachStderr:    true,
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
	return Plan{
		valid:        true,
		name:         "loki-job-" + spec.ID,
		policySHA256: p.generationSHA256,
		create:       create,
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
	return p.valid && p.name != "" && digestPattern.MatchString(p.policySHA256) && p.create.Image != ""
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
