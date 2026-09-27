package windows

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func ResolveInstallOptionsWithArgs(args []string, lookup EnvironmentLookup) (InstallOptions, error) {
	options, err := ResolveInstallOptions(lookup)
	if err != nil {
		return InstallOptions{}, err
	}
	flags := flag.NewFlagSet("loki install", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	distribution := flags.String("distribution", options.Distribution, "WSL distribution name")
	location := flags.String("location", options.InstallLocation, "custom WSL install location")
	autoStart := flags.String("autostart", strconv.FormatBool(options.AutoStart), "enable Windows logon startup")
	appliance := flags.String("appliance-file", options.LocalAppliance, "local WSL appliance file")
	port := flags.String("mcp-port", strconv.Itoa(options.MCPPort), "MCP loopback port")
	reinstall := flags.Bool("reinstall", options.ReinstallRequested, "approve destructive stale reinstall")
	if err := flags.Parse(args); err != nil {
		return InstallOptions{}, err
	}
	if flags.NArg() != 0 {
		return InstallOptions{}, fmt.Errorf("unexpected install arguments: %v", flags.Args())
	}

	visited := map[string]bool{}
	flags.Visit(func(item *flag.Flag) { visited[item.Name] = true })
	if visited["distribution"] {
		options.Distribution = *distribution
	}
	if visited["location"] {
		options.InstallLocation = *location
		options.InstallLocationExplicit = true
	}
	if visited["autostart"] {
		value, parseErr := strconv.ParseBool(*autoStart)
		if parseErr != nil {
			return InstallOptions{}, errors.New("--autostart must be true or false")
		}
		options.AutoStart = value
		options.AutoStartExplicit = true
	}
	if visited["appliance-file"] {
		options.LocalAppliance = *appliance
	}
	if visited["mcp-port"] {
		value, parseErr := strconv.Atoi(*port)
		if parseErr != nil || value < 1024 || value > 65535 {
			return InstallOptions{}, errors.New("--mcp-port must be an integer between 1024 and 65535")
		}
		options.MCPPort = value
		options.MCPPortExplicit = true
	}
	if visited["reinstall"] {
		options.ReinstallRequested = *reinstall
	}
	if !distributionNamePattern.MatchString(options.Distribution) {
		return InstallOptions{}, errors.New("--distribution must be 1-64 characters using letters, digits, dot, underscore, or hyphen")
	}
	if options.InstallLocation != "" {
		normalized, ok := normalizeWindowsPath(options.InstallLocation)
		if !ok {
			return InstallOptions{}, errors.New("--location must be an absolute Windows path")
		}
		options.InstallLocation = strings.ReplaceAll(normalized, "/", "\\")
	}
	return options, nil
}
