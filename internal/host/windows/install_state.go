package windows

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
)

type WindowsStateKind string

const (
	WindowsStateAbsent     WindowsStateKind = "absent"
	WindowsStateManifest   WindowsStateKind = "manifest"
	WindowsStateLegacy     WindowsStateKind = "legacy"
	WindowsStateUnverified WindowsStateKind = "unverified"
)

type WindowsState struct {
	Present         bool
	Owned           bool
	Kind            WindowsStateKind
	InstallLocation string
	AutoStart       bool
	AutoStartKnown  bool
	MCPPort         int
}

type ExpectedInstallation struct {
	Distribution   string
	StateDir       string
	TaskName       string
	TaskExecutable string
	TaskArguments  string
}

type ownershipManifestDisk struct {
	SchemaVersion   int    `json:"schema_version"`
	Distribution    string `json:"distribution"`
	ReleaseTag      string `json:"release_tag"`
	StateDir        string `json:"state_dir"`
	InstallLocation string `json:"install_location"`
	AutoStart       bool   `json:"autostart"`
	MCPPort         int    `json:"mcp_port"`
	TaskName        string `json:"task_name"`
	TaskExecutable  string `json:"task_executable"`
	TaskArguments   string `json:"task_arguments"`
}

var (
	windowsReleaseTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
	applianceVersionPattern  = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
)

func ParseOwnershipManifest(raw []byte, expected ExpectedInstallation) (WindowsState, error) {
	var manifest ownershipManifestDisk
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return WindowsState{}, fmt.Errorf("decode ownership manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 ||
		!strings.EqualFold(manifest.Distribution, expected.Distribution) ||
		!WindowsPathEqual(manifest.StateDir, expected.StateDir) ||
		manifest.TaskName != expected.TaskName ||
		!WindowsPathEqual(manifest.TaskExecutable, expected.TaskExecutable) ||
		manifest.TaskArguments != expected.TaskArguments ||
		!windowsReleaseTagPattern.MatchString(manifest.ReleaseTag) {
		return WindowsState{}, errors.New("ownership manifest does not match the requested Loki installation")
	}
	installLocation := ""
	if manifest.InstallLocation != "" {
		normalized, ok := normalizeWindowsPath(manifest.InstallLocation)
		if !ok || !SafeOwnedInstallLocation(normalized, expected.StateDir) {
			return WindowsState{}, errors.New("ownership manifest contains an unsafe install location")
		}
		installLocation = strings.ReplaceAll(normalized, "/", "\\")
	}
	if manifest.MCPPort < 1024 || manifest.MCPPort > 65535 {
		return WindowsState{}, errors.New("ownership manifest contains an invalid MCP port")
	}
	return WindowsState{
		Present: true, Owned: true, Kind: WindowsStateManifest,
		InstallLocation: installLocation,
		AutoStart:       manifest.AutoStart, AutoStartKnown: true,
		MCPPort: manifest.MCPPort,
	}, nil
}

type legacyLocalOrigin struct {
	URL            string `json:"url"`
	Transport      string `json:"transport"`
	Reachability   string `json:"reachability"`
	Authentication struct {
		Type      string `json:"type"`
		TokenFile string `json:"token_file"`
	} `json:"authentication"`
}

type legacyConnection struct {
	SchemaVersion  *int               `json:"schema_version"`
	LocalOrigin    *legacyLocalOrigin `json:"local_origin"`
	Endpoint       *string            `json:"endpoint"`
	Transport      *string            `json:"transport"`
	Authentication *string            `json:"authentication"`
	TokenFile      *string            `json:"token_file"`
	Distribution   string             `json:"distribution"`
}

func ParseLegacyConnection(raw []byte, expected ExpectedInstallation) (WindowsState, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return WindowsState{}, fmt.Errorf("decode legacy connection state: %w", err)
	}
	var connection legacyConnection
	if err := json.Unmarshal(raw, &connection); err != nil {
		return WindowsState{}, fmt.Errorf("decode legacy connection state: %w", err)
	}
	if !strings.EqualFold(connection.Distribution, expected.Distribution) {
		return WindowsState{}, errors.New("legacy connection state does not match the requested Loki distribution")
	}
	tokenFile := joinWindowsPath(expected.StateDir, "mcp-token")
	if connection.LocalOrigin != nil {
		if connection.SchemaVersion == nil || *connection.SchemaVersion != 1 ||
			!WindowsPathEqual(connection.LocalOrigin.Authentication.TokenFile, tokenFile) ||
			connection.LocalOrigin.Authentication.Type != "bearer-token-file" ||
			connection.LocalOrigin.Transport != "streamable-http" ||
			connection.LocalOrigin.Reachability != "loopback" {
			return WindowsState{}, errors.New("legacy local-origin connection state does not prove Loki ownership")
		}
		origin, port, err := parseLoopbackOrigin(connection.LocalOrigin.URL, false)
		if err != nil {
			return WindowsState{}, err
		}
		if connection.Authentication != nil && *connection.Authentication != "bearer-token-file" {
			return WindowsState{}, errors.New("legacy authentication alias does not match Loki connection state")
		}
		if connection.TokenFile != nil && !WindowsPathEqual(*connection.TokenFile, tokenFile) {
			return WindowsState{}, errors.New("legacy token-file alias does not match Loki connection state")
		}
		if connection.Endpoint != nil && *connection.Endpoint != origin {
			return WindowsState{}, errors.New("legacy endpoint alias does not match Loki connection state")
		}
		if connection.Transport != nil && *connection.Transport != "streamable-http" {
			return WindowsState{}, errors.New("legacy transport alias does not match Loki connection state")
		}
		return WindowsState{Present: true, Owned: true, Kind: WindowsStateLegacy, MCPPort: port}, nil
	}

	allowed := map[string]bool{
		"endpoint":       true,
		"transport":      true,
		"authentication": true,
		"token_file":     true,
		"distribution":   true,
	}
	if len(fields) != len(allowed) {
		return WindowsState{}, errors.New("legacy flat connection state does not prove Loki ownership")
	}
	for name := range fields {
		if !allowed[name] {
			return WindowsState{}, errors.New("legacy flat connection state does not prove Loki ownership")
		}
	}
	if connection.Endpoint == nil || connection.Transport == nil ||
		connection.Authentication == nil || connection.TokenFile == nil ||
		*connection.Transport != "streamable-http" ||
		*connection.Authentication != "bearer-token-file" ||
		!WindowsPathEqual(*connection.TokenFile, tokenFile) {
		return WindowsState{}, errors.New("legacy flat connection state does not prove Loki ownership")
	}
	_, port, err := parseLoopbackOrigin(*connection.Endpoint, true)
	if err != nil {
		return WindowsState{}, err
	}
	return WindowsState{Present: true, Owned: true, Kind: WindowsStateLegacy, MCPPort: port}, nil
}

func parseLoopbackOrigin(raw string, requireHistoricalPort bool) (string, int, error) {
	origin, err := url.Parse(raw)
	if err != nil || origin.Scheme != "http" || origin.Hostname() != "127.0.0.1" ||
		origin.EscapedPath() != "/mcp" || origin.RawQuery != "" || origin.Fragment != "" ||
		origin.User != nil || origin.Port() == "" {
		return "", 0, errors.New("legacy connection state is not a valid Loki loopback origin")
	}
	port, err := strconv.Atoi(origin.Port())
	if err != nil || port < 1024 || port > 65535 || (requireHistoricalPort && port != 18765) {
		return "", 0, errors.New("legacy connection state is not a valid Loki loopback origin")
	}
	return origin.String(), port, nil
}

func WindowsPathEqual(left, right string) bool {
	leftPath, leftOK := normalizeWindowsPath(left)
	rightPath, rightOK := normalizeWindowsPath(right)
	return leftOK && rightOK && strings.EqualFold(leftPath, rightPath)
}

func SafeOwnedInstallLocation(location, stateDir string) bool {
	owned, ok := normalizeWindowsPath(location)
	if !ok {
		return false
	}
	state, ok := normalizeWindowsPath(stateDir)
	if !ok {
		return false
	}
	root := windowsVolumeRoot(owned)
	if root == "" || strings.EqualFold(owned, root) ||
		strings.EqualFold(owned, state) ||
		windowsPathWithin(owned, state) ||
		windowsPathWithin(state, owned) {
		return false
	}
	return true
}

func normalizeWindowsPath(raw string) (string, bool) {
	value := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if value == "" || strings.ContainsRune(value, 0) {
		return "", false
	}
	if len(value) >= 3 && isASCIIAlpha(value[0]) && value[1] == ':' && value[2] == '/' {
		clean := path.Clean(value)
		if len(clean) < 3 || clean[1] != ':' || clean[2] != '/' {
			return "", false
		}
		if len(clean) == 3 {
			return clean, true
		}
		return strings.TrimSuffix(clean, "/"), true
	}
	if strings.HasPrefix(value, "//") {
		parts := strings.Split(strings.TrimPrefix(value, "//"), "/")
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" ||
			parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." {
			return "", false
		}
		root := "//" + parts[0] + "/" + parts[1]
		stack := make([]string, 0, len(parts)-2)
		for _, component := range parts[2:] {
			switch component {
			case "", ".":
				continue
			case "..":
				if len(stack) == 0 {
					return "", false
				}
				stack = stack[:len(stack)-1]
			default:
				stack = append(stack, component)
			}
		}
		if len(stack) == 0 {
			return root, true
		}
		return root + "/" + strings.Join(stack, "/"), true
	}
	return "", false
}

func windowsVolumeRoot(normalized string) string {
	if len(normalized) >= 3 && normalized[1] == ':' && normalized[2] == '/' {
		return normalized[:3]
	}
	if strings.HasPrefix(normalized, "//") {
		parts := strings.Split(strings.TrimPrefix(normalized, "//"), "/")
		if len(parts) >= 2 {
			return "//" + parts[0] + "/" + parts[1]
		}
	}
	return ""
}

func windowsPathWithin(candidate, parent string) bool {
	candidateLower := strings.ToLower(strings.TrimSuffix(candidate, "/"))
	parentLower := strings.ToLower(strings.TrimSuffix(parent, "/"))
	return strings.HasPrefix(candidateLower, parentLower+"/")
}

func joinWindowsPath(base, name string) string {
	return strings.TrimRight(base, "\\/") + "\\" + name
}

func isASCIIAlpha(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

type DistributionStateKind string

const (
	DistributionAbsent        DistributionStateKind = "absent"
	DistributionForeign       DistributionStateKind = "foreign"
	DistributionIndeterminate DistributionStateKind = "indeterminate"
	DistributionProvisioning  DistributionStateKind = "provisioning"
	DistributionHealthy       DistributionStateKind = "healthy"
	DistributionStale         DistributionStateKind = "stale"
)

type DistributionState struct {
	State   DistributionStateKind
	Version string
}

type NativeProbe struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

type DistributionProbe struct {
	Present      bool
	Manifest     NativeProbe
	Version      NativeProbe
	Provisioned  NativeProbe
	Doctor       NativeProbe
	Connection   NativeProbe
	ServiceState NativeProbe
}

func ClassifyDistribution(probe DistributionProbe) DistributionState {
	if !probe.Present {
		return DistributionState{State: DistributionAbsent}
	}
	version, owned := distributionIdentity(probe.Manifest, probe.Version)
	if !owned {
		return DistributionState{State: DistributionForeign}
	}
	if probe.Provisioned.ExitCode == 0 {
		if probe.Doctor.ExitCode == 0 && probe.Connection.ExitCode == 0 {
			return DistributionState{State: DistributionHealthy, Version: version}
		}
		return DistributionState{State: DistributionStale, Version: version}
	}
	if probe.Provisioned.ExitCode != 1 || probe.ServiceState.ExitCode != 0 {
		return DistributionState{State: DistributionIndeterminate, Version: version}
	}
	service := parseSystemdProperties(probe.ServiceState.Stdout)
	active := service["ActiveState"]
	sub := service["SubState"]
	restarts, _ := strconv.Atoi(service["NRestarts"])
	if active == "activating" || active == "active" ||
		sub == "start" || sub == "running" || sub == "auto-restart" {
		return DistributionState{State: DistributionProvisioning, Version: version}
	}
	if active == "failed" || restarts >= 3 {
		return DistributionState{State: DistributionStale, Version: version}
	}
	return DistributionState{State: DistributionStale, Version: version}
}

func parseSystemdProperties(raw string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			values[parts[0]] = parts[1]
		}
	}
	return values
}

type StartupTaskState struct {
	Present bool
	Owned   bool
}

type ExistingSnapshot struct {
	Distribution DistributionState
	Windows      WindowsState
	StartupTask  StartupTaskState
}

type ExistingAction string

const (
	ExistingFresh              ExistingAction = "fresh"
	ExistingHealthyNoop        ExistingAction = "healthy-noop"
	ExistingRemoveOrphan       ExistingAction = "remove-orphan"
	ExistingStaleNeedsApproval ExistingAction = "stale-needs-approval"
	ExistingBlocked            ExistingAction = "blocked"
)

type ExistingAssessment struct {
	Action ExistingAction
	Reason string
}

func AssessExistingInstallation(snapshot ExistingSnapshot) ExistingAssessment {
	if snapshot.Windows.Present && !snapshot.Windows.Owned {
		return ExistingAssessment{Action: ExistingBlocked, Reason: "unverified-windows-state"}
	}
	if snapshot.StartupTask.Present && !snapshot.StartupTask.Owned {
		return ExistingAssessment{Action: ExistingBlocked, Reason: "unverified-startup-task"}
	}
	switch snapshot.Distribution.State {
	case DistributionForeign:
		return ExistingAssessment{Action: ExistingBlocked, Reason: "foreign-distribution"}
	case DistributionIndeterminate:
		return ExistingAssessment{Action: ExistingBlocked, Reason: "indeterminate-distribution"}
	case DistributionProvisioning:
		return ExistingAssessment{Action: ExistingBlocked, Reason: "distribution-provisioning"}
	case DistributionHealthy:
		if !snapshot.Windows.Present {
			return ExistingAssessment{Action: ExistingBlocked, Reason: "healthy-missing-windows-state"}
		}
		if snapshot.Windows.Kind == WindowsStateManifest {
			if snapshot.Windows.AutoStart && !snapshot.StartupTask.Present {
				return ExistingAssessment{Action: ExistingBlocked, Reason: "healthy-missing-startup-task"}
			}
			if !snapshot.Windows.AutoStart && snapshot.StartupTask.Present {
				return ExistingAssessment{Action: ExistingBlocked, Reason: "healthy-unexpected-startup-task"}
			}
		}
		return ExistingAssessment{Action: ExistingHealthyNoop}
	case DistributionStale:
		return ExistingAssessment{Action: ExistingStaleNeedsApproval}
	case DistributionAbsent:
		if snapshot.Windows.Present || snapshot.StartupTask.Present {
			return ExistingAssessment{Action: ExistingRemoveOrphan}
		}
		return ExistingAssessment{Action: ExistingFresh}
	default:
		return ExistingAssessment{Action: ExistingBlocked, Reason: "unsupported-distribution-state"}
	}
}

type InstallSettings struct {
	MCPPort                 int
	MCPPortExplicit         bool
	AutoStart               bool
	AutoStartExplicit       bool
	InstallLocation         string
	InstallLocationExplicit bool
}

func PreserveOwnedSettings(requested InstallSettings, state WindowsState) InstallSettings {
	if !state.Owned {
		return requested
	}
	if !requested.MCPPortExplicit && state.MCPPort >= 1024 && state.MCPPort <= 65535 {
		requested.MCPPort = state.MCPPort
	}
	if state.Kind == WindowsStateManifest {
		if !requested.AutoStartExplicit {
			requested.AutoStart = state.AutoStart
		}
		if !requested.InstallLocationExplicit && state.InstallLocation != "" {
			requested.InstallLocation = state.InstallLocation
		}
	}
	return requested
}
