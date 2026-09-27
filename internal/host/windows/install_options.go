package windows

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type EnvironmentLookup func(string) (string, bool)

type InstallOptions struct {
	Distribution            string
	InstallLocation         string
	InstallLocationExplicit bool
	AutoStart               bool
	AutoStartExplicit       bool
	LocalAppliance          string
	MCPPort                 int
	MCPPortExplicit         bool
	ReinstallRequested      bool
}

var distributionNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func ResolveInstallOptions(lookup EnvironmentLookup) (InstallOptions, error) {
	value := func(name string) (string, bool) {
		raw, ok := lookup(name)
		return strings.TrimSpace(raw), ok
	}
	distribution, _ := value("LOKI_WSL_NAME")
	if distribution == "" {
		distribution = "loki-mcp"
	}
	if !distributionNamePattern.MatchString(distribution) {
		return InstallOptions{}, errors.New("LOKI_WSL_NAME must be 1-64 characters using letters, digits, dot, underscore, or hyphen")
	}
	location, locationPresent := value("LOKI_WSL_LOCATION")
	locationExplicit := locationPresent && location != ""
	if locationExplicit {
		normalized, ok := normalizeWindowsPath(location)
		if !ok {
			return InstallOptions{}, errors.New("LOKI_WSL_LOCATION must be an absolute Windows path")
		}
		location = strings.ReplaceAll(normalized, "/", "\\")
	}
	autoStartRaw, autoStartPresent := lookup("LOKI_WSL_AUTOSTART")
	autoStart := true
	if autoStartPresent {
		autoStart = strings.TrimSpace(autoStartRaw) != "0"
	}
	appliance, _ := value("LOKI_WSL_APPLIANCE_FILE")
	portText, portPresent := value("LOKI_MCP_PORT")
	portExplicit := portPresent && portText != ""
	if !portExplicit {
		portText = "18765"
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1024 || port > 65535 {
		return InstallOptions{}, errors.New("LOKI_MCP_PORT must be an integer between 1024 and 65535")
	}
	reinstall, _ := value("LOKI_WSL_REINSTALL")
	return InstallOptions{
		Distribution:            distribution,
		InstallLocation:         location,
		InstallLocationExplicit: locationExplicit,
		AutoStart:               autoStart,
		AutoStartExplicit:       autoStartPresent,
		LocalAppliance:          appliance,
		MCPPort:                 port,
		MCPPortExplicit:         portExplicit,
		ReinstallRequested:      reinstall == "1",
	}, nil
}

func (options InstallOptions) Settings() InstallSettings {
	return InstallSettings{
		MCPPort:                 options.MCPPort,
		MCPPortExplicit:         options.MCPPortExplicit,
		AutoStart:               options.AutoStart,
		AutoStartExplicit:       options.AutoStartExplicit,
		InstallLocation:         options.InstallLocation,
		InstallLocationExplicit: options.InstallLocationExplicit,
	}
}

func (options *InstallOptions) ApplyPreserved(state WindowsState) {
	settings := PreserveOwnedSettings(options.Settings(), state)
	options.MCPPort = settings.MCPPort
	options.AutoStart = settings.AutoStart
	options.InstallLocation = settings.InstallLocation
}

func ExpectedFromOptions(options InstallOptions, localAppData, systemRoot string) (ExpectedInstallation, error) {
	if !distributionNamePattern.MatchString(options.Distribution) {
		return ExpectedInstallation{}, errors.New("Windows Loki distribution name is invalid")
	}
	localRoot, ok := normalizeWindowsPath(localAppData)
	if !ok {
		return ExpectedInstallation{}, errors.New("Windows LOCALAPPDATA must be an absolute Windows path")
	}
	systemRootPath, ok := normalizeWindowsPath(systemRoot)
	if !ok {
		return ExpectedInstallation{}, errors.New("Windows SystemRoot must be an absolute Windows path")
	}
	localRoot = strings.ReplaceAll(localRoot, "/", "\\")
	systemRootPath = strings.ReplaceAll(systemRootPath, "/", "\\")
	stateDir := joinWindowsPath(joinWindowsPath(localRoot, "Loki"), options.Distribution)
	taskName := "Loki WSL (" + options.Distribution + ")"
	return ExpectedInstallation{
		Distribution:   options.Distribution,
		StateDir:       stateDir,
		TaskName:       taskName,
		TaskExecutable: joinWindowsPath(systemRootPath, "System32\\wsl.exe"),
		TaskArguments:  fmt.Sprintf("-d %s --exec /usr/bin/sleep infinity", options.Distribution),
	}, nil
}
