package releases

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

const maxHostIdentityBytes = 64 << 10

func DetectHost() (SupportedHost, error) {
	osRelease, err := readHostIdentityFile("/etc/os-release", maxHostIdentityBytes)
	if err != nil {
		return SupportedHost{}, err
	}
	kernelRelease, err := readHostIdentityFile("/proc/sys/kernel/osrelease", maxHostIdentityBytes)
	if err != nil {
		return SupportedHost{}, err
	}
	return detectHost(runtime.GOOS, runtime.GOARCH, osRelease, kernelRelease)
}

func detectHost(goos, goarch string, osRelease, kernelRelease []byte) (SupportedHost, error) {
	if goos != "linux" || goarch != "amd64" {
		return SupportedHost{}, fmt.Errorf("unsupported host platform %s/%s", goos, goarch)
	}
	values, err := parseHostOSRelease(osRelease)
	if err != nil {
		return SupportedHost{}, err
	}
	distribution := strings.ToLower(values["ID"])
	version := values["VERSION_ID"]
	if distribution != "ubuntu" || version != "24.04" {
		return SupportedHost{}, fmt.Errorf("unsupported host %s %s", distribution, version)
	}
	environment := "native"
	if strings.Contains(strings.ToLower(string(kernelRelease)), "microsoft") {
		environment = "wsl"
	}
	return SupportedHost{
		Environment: environment, Distribution: distribution, Version: version, Arch: goarch,
	}, nil
}

func parseHostOSRelease(raw []byte) (map[string]string, error) {
	if len(raw) == 0 || len(raw) > maxHostIdentityBytes {
		return nil, errors.New("os-release exceeds host identity size policy")
	}
	result := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = value[1 : len(value)-1]
			value = strings.ReplaceAll(value, `\"`, `"`)
			value = strings.ReplaceAll(value, `\\`, `\`)
		}
		if key == "ID" || key == "VERSION_ID" {
			if value == "" || strings.ContainsAny(value, "\r\n\x00") {
				return nil, errors.New("os-release contains invalid host identity")
			}
			result[key] = value
		}
	}
	if result["ID"] == "" || result["VERSION_ID"] == "" {
		return nil, errors.New("os-release is missing host identity")
	}
	return result, nil
}

func readHostIdentityFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("host identity file exceeds size policy")
	}
	return bytes.Clone(raw), nil
}
