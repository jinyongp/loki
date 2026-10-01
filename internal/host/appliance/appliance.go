// Package appliance owns the Linux requirements of the managed WSL appliance.
// Build, diagnostics, and approved repair use the same declarative contract.
package appliance

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"loki/internal/host/diagnostics"
)

//go:embed requirements.tsv
var Requirements string

type requirement struct {
	Package, Version, Path, Kind, Unit, Peer string
}

func requirements() []requirement {
	var result []requirement
	for _, line := range strings.Split(Requirements, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 6 {
			panic("invalid embedded WSL requirement")
		}
		result = append(result, requirement{fields[0], fields[1], fields[2], fields[3], fields[4], fields[5]})
	}
	return result
}

func Revision() string { return fmt.Sprintf("%x", sha256.Sum256([]byte(Requirements))) }

type Runner func(context.Context, string, ...string) (string, error)

func Exec(ctx context.Context, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = append(os.Environ(), "LC_ALL=C", "DEBIAN_FRONTEND=noninteractive")
	raw, err := command.CombinedOutput()
	if err != nil {
		return string(raw), fmt.Errorf("%s failed: %w: %s", filepath.Base(name), err, strings.TrimSpace(string(raw)))
	}
	return strings.TrimSpace(string(raw)), nil
}

type Host struct {
	// Root is used by offline artifact checks and test fixtures. Runtime uses /.
	Root string
	Run  Runner
}

func (h Host) path(path string) string { return filepath.Join(h.Root, path) }

func (h Host) run(ctx context.Context, name string, args ...string) (string, error) {
	if h.Run != nil {
		return h.Run(ctx, name, args...)
	}
	return Exec(ctx, name, args...)
}

// Managed limits OS inspection and repair to Loki's WSL image. Other Linux
// hosts keep their existing diagnostics and prerequisite installation policy.
func (h Host) Managed() (bool, error) {
	info, err := os.Lstat(h.path("/usr/lib/loki-appliance/release-manifest.json"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("WSL appliance identity must be a regular file")
	}
	kernel, err := os.ReadFile(h.path("/proc/sys/kernel/osrelease"))
	if err != nil {
		return false, err
	}
	return strings.Contains(strings.ToLower(string(kernel)), "microsoft"), nil
}

// MissingFiles also works without systemd or root, against an extracted image.
func (h Host) MissingFiles() []string {
	var missing []string
	for _, item := range requirements() {
		info, err := os.Stat(h.path(item.Path))
		if err != nil || !info.Mode().IsRegular() || (item.Kind == "executable" && info.Mode()&0111 == 0) {
			missing = append(missing, item.Path)
		}
	}
	return missing
}

func (h Host) packageVersion(ctx context.Context, name string) (string, error) {
	raw, err := h.run(ctx, "/usr/bin/dpkg-query", "-W", "-f=${db:Status-Status}\t${Version}", name)
	fields := strings.Split(strings.TrimSpace(raw), "\t")
	if err != nil || len(fields) != 2 || fields[0] != "installed" || fields[1] == "" {
		return "", fmt.Errorf("required package %s is not installed", name)
	}
	return fields[1], nil
}

func (h Host) packageProblems(ctx context.Context) ([]string, map[string]string) {
	versions := make(map[string]string)
	var problems []string
	for _, item := range requirements() {
		if _, found := versions[item.Package]; found {
			continue
		}
		version, err := h.packageVersion(ctx, item.Package)
		versions[item.Package] = version
		if err != nil {
			problems = append(problems, item.Package)
		}
	}
	for _, item := range requirements() {
		if item.Peer != "-" && versions[item.Package] != "" && versions[item.Package] != versions[item.Peer] {
			problems = append(problems, item.Package+"/"+item.Peer+" version mismatch")
		}
	}
	return problems, versions
}

func units() []string {
	seen := make(map[string]bool)
	var result []string
	for _, item := range requirements() {
		if item.Unit != "-" && !seen[item.Unit] {
			seen[item.Unit] = true
			result = append(result, item.Unit)
		}
	}
	return result
}

func (h Host) unitReady(ctx context.Context, unit string) bool {
	raw, err := h.run(ctx, "/usr/bin/systemctl", "show", unit, "--property=LoadState,ActiveState,Result,ConditionResult", "--no-pager")
	if err != nil {
		return false
	}
	properties := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		key, value, found := strings.Cut(line, "=")
		if found {
			properties[key] = value
		}
	}
	if properties["LoadState"] != "loaded" || properties["Result"] != "success" {
		return false
	}
	if unit == "kmod-static-nodes.service" && properties["ConditionResult"] == "no" && properties["ActiveState"] == "inactive" {
		return true // Some WSL kernels do not publish modules.devname.
	}
	return properties["ActiveState"] == "active"
}

func evidence(name string, values []string) diagnostics.Evidence {
	value := strings.Join(values, ",")
	if value == "" {
		value = "none"
	}
	if len(value) > 240 {
		value = value[:240] + "..."
	}
	return diagnostics.Evidence{Name: name, Value: value}
}

func (h Host) Inspect(ctx context.Context) []diagnostics.Check {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	problems, _ := h.packageProblems(ctx)
	if raw, err := h.run(ctx, "/usr/bin/dpkg", "--audit"); err != nil || strings.TrimSpace(raw) != "" {
		problems = append(problems, "package database incomplete")
	}
	files := h.MissingFiles()
	checks := []diagnostics.Check{diagnostics.Healthy("wsl.prerequisites", "wsl_prerequisites_ready", "WSL boot prerequisites are installed",
		diagnostics.Evidence{Name: "requirements_revision", Value: Revision()})}
	if len(problems) != 0 || len(files) != 0 {
		checks[0] = diagnostics.Blocked("wsl.prerequisites", "wsl_prerequisites_missing", "WSL boot prerequisites need approved repair",
			evidence("packages", problems), evidence("files", files))
	}
	var unhealthy []string
	for _, unit := range units() {
		if !h.unitReady(ctx, unit) {
			unhealthy = append(unhealthy, unit)
		}
	}
	if len(unhealthy) == 0 {
		checks = append(checks, diagnostics.Healthy("wsl.services", "wsl_services_ready", "WSL boot services are ready"))
	} else {
		checks = append(checks, diagnostics.Blocked("wsl.services", "wsl_services_unhealthy", "WSL boot services are not ready", evidence("units", unhealthy)))
	}
	raw, err := h.run(ctx, "/usr/sbin/runuser", "-u", "ubuntu", "--", "/usr/bin/env", "XDG_RUNTIME_DIR=/run/user/1000", "/usr/bin/systemctl", "--user", "is-active", "default.target")
	if err != nil || strings.TrimSpace(raw) != "active" {
		checks = append(checks, diagnostics.Blocked("wsl.user", "wsl_user_session_unhealthy", "default WSL user systemd session is not ready"))
	} else {
		checks = append(checks, diagnostics.Healthy("wsl.user", "wsl_user_session_ready", "default WSL user systemd session is active"))
	}
	raw, err = h.run(ctx, "/usr/bin/systemctl", "--failed", "--no-legend", "--plain", "--no-pager")
	var failed []string
	for _, line := range strings.Split(raw, "\n") {
		if fields := strings.Fields(line); len(fields) != 0 {
			failed = append(failed, fields[0])
		}
	}
	if err != nil || len(failed) != 0 {
		checks = append(checks, diagnostics.Blocked("wsl.boot", "wsl_boot_unhealthy", "WSL boot has failed units or cannot be inspected", evidence("units", failed)))
	} else {
		checks = append(checks, diagnostics.Healthy("wsl.boot", "wsl_boot_ready", "WSL boot has no failed systemd units"))
	}
	return checks
}

// Repair reconciles actual state on every invocation. There is no once-only
// migration flag: a later package removal is detected and repaired again.
// Existing versions are preserved; PAM is always paired with installed systemd.
func (h Host) Repair(ctx context.Context) error {
	problems, versions := h.packageProblems(ctx)
	missing := h.MissingFiles()
	wanted := make(map[string]string)
	for _, item := range requirements() {
		needsRepair := versions[item.Package] == ""
		for _, path := range missing {
			needsRepair = needsRepair || path == item.Path
		}
		if item.Peer != "-" && versions[item.Package] != versions[item.Peer] {
			needsRepair = true
		}
		if !needsRepair {
			continue
		}
		version := versions[item.Package]
		if version == "" {
			version = item.Version
		}
		if item.Peer != "-" {
			if versions[item.Peer] == "" {
				return fmt.Errorf("cannot repair %s without installed %s", item.Package, item.Peer)
			}
			version = versions[item.Peer]
		}
		wanted[item.Package] = item.Package + "=" + version
	}
	if len(wanted) != 0 {
		if _, err := h.run(ctx, "/usr/bin/apt-get", "update"); err != nil {
			return err
		}
		args := []string{"install", "-y", "--no-install-recommends", "--no-remove", "--no-upgrade", "--reinstall"}
		var packages []string
		for _, value := range wanted {
			packages = append(packages, value)
		}
		sort.Strings(packages)
		if _, err := h.run(ctx, "/usr/bin/apt-get", append(args, packages...)...); err != nil {
			return err
		}
		problems, _ = h.packageProblems(ctx)
		if len(problems) != 0 || len(h.MissingFiles()) != 0 {
			return errors.New("WSL prerequisite repair did not restore required packages and files")
		}
		if _, err := h.run(ctx, "/usr/bin/systemctl", "daemon-reload"); err != nil {
			return err
		}
	}
	for _, unit := range units() {
		if h.unitReady(ctx, unit) {
			continue
		}
		// Reset only the prerequisite being repaired. Other failures remain visible.
		if _, err := h.run(ctx, "/usr/bin/systemctl", "reset-failed", unit); err != nil {
			return err
		}
		if _, err := h.run(ctx, "/usr/bin/systemctl", "start", unit); err != nil {
			return err
		}
	}
	for _, check := range h.Inspect(ctx) {
		if check.Status != diagnostics.StatusHealthy {
			return fmt.Errorf("%s: %s", check.Code, check.Summary)
		}
	}
	return nil
}
