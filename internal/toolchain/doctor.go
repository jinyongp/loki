package toolchain

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type Report struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

type Check struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
}

func Doctor(ctx context.Context, manifest Manifest, root string, checkApt bool) Report {
	report := Report{OK: true}
	add := func(check Check) { report.Checks = append(report.Checks, check); report.OK = report.OK && check.OK }
	platform := readOSRelease(rooted(root, "/etc/os-release"))
	actualPlatform := platform["ID"] + ":" + platform["VERSION_ID"]
	wantPlatform := manifest.Platform.ID + ":" + manifest.Platform.Version
	add(Check{Name: "platform", OK: actualPlatform == wantPlatform, Expected: wantPlatform, Actual: actualPlatform})
	actualArch := manifest.Platform.Arch
	if root == "/" {
		actualArch = runtime.GOARCH
	}
	add(Check{Name: "architecture", OK: actualArch == manifest.Platform.Arch, Expected: manifest.Platform.Arch, Actual: actualArch})
	if checkApt {
		for _, item := range manifest.AptPackages {
			command := exec.CommandContext(ctx, "dpkg-query", "--root="+root, "-W", "-f=${Version}", item.Name)
			output, err := command.Output()
			actual := strings.TrimSpace(string(output))
			add(Check{Name: "apt:" + item.Name, OK: err == nil && actual == item.Version, Expected: item.Version, Actual: actual})
		}
	}
	for _, artifact := range manifest.Artifacts {
		target := rooted(root, artifact.InstallPath)
		marker := target + ".loki-artifact.json"
		if artifact.Format != "file" {
			marker = filepath.Join(target, ".loki-artifact.json")
		}
		var installed installedArtifact
		raw, err := os.ReadFile(marker)
		if err == nil {
			err = json.Unmarshal(raw, &installed)
		}
		actualDigest, digestErr := installedDigest(target, artifact.Format)
		ok := err == nil && digestErr == nil && installed.Name == artifact.Name && installed.Version == artifact.Version && installed.SHA256 == artifact.SHA256 && installed.TreeSHA256 == actualDigest
		add(Check{Name: "artifact:" + artifact.Name, OK: ok, Expected: artifact.Version + ":" + artifact.SHA256, Actual: installed.Version + ":" + installed.SHA256})
		for name, relative := range artifact.Links {
			expected := artifact.InstallPath
			if relative != "." {
				expected = filepath.Join(expected, relative)
			}
			actual, linkErr := os.Readlink(rooted(root, filepath.Join("/opt/loki/toolchain/bin", name)))
			add(Check{Name: "link:" + name, OK: linkErr == nil && actual == expected, Expected: expected, Actual: actual})
		}
	}
	return report
}

func readOSRelease(path string) map[string]string {
	values := map[string]string{}
	file, err := os.Open(path)
	if err != nil {
		return values
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		name, value, found := strings.Cut(scanner.Text(), "=")
		if found {
			values[name] = strings.Trim(value, `"'`)
		}
	}
	return values
}

func (r Report) Error() error {
	if r.OK {
		return nil
	}
	for _, check := range r.Checks {
		if !check.OK {
			return fmt.Errorf("toolchain check %s failed", check.Name)
		}
	}
	return fmt.Errorf("toolchain doctor failed")
}
