package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/host/releases"
)

func readyWorkspaceACL() string {
	return strings.Join([]string{
		"user::rwx",
		"user:10000:rwx",
		"group::r-x",
		"mask::rwx",
		"other::---",
		"default:user::rwx",
		"default:user:10000:rwx",
		"default:group::r-x",
		"default:mask::rwx",
		"default:other::---",
		"",
	}, "\n")
}

func TestResolveInstallWorkspaceRequiresExplicitInputWhenNonInteractive(t *testing.T) {
	options := hostInstallOptions{
		StateRoot:       filepath.Join(t.TempDir(), "state"),
		ReleaseManifest: filepath.Join(t.TempDir(), "release-manifest.json"),
	}
	executor := &fakeHostExecutor{paths: map[string]bool{}, outputs: map[string]string{}, errs: map[string]error{}}
	err := resolveInstallWorkspace(
		t.Context(), &options, newHostInstallPrompter(strings.NewReader(""), &bytes.Buffer{}, false), executor,
	)
	if err == nil || !strings.Contains(err.Error(), "--workspace is required") {
		t.Fatalf("non-interactive missing workspace error = %v", err)
	}
}

func TestResolveInstallWorkspaceInteractiveCreatesAndPreparesACL(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	stateRoot := filepath.Join(t.TempDir(), "state")
	manifestRoot := t.TempDir()
	manifest := filepath.Join(manifestRoot, "release-manifest.json")

	executor := &fakeHostExecutor{
		paths:   map[string]bool{"install": true, "getfacl": true, "setfacl": true},
		outputs: map[string]string{},
		errs:    map[string]error{},
	}
	getfacl := "getfacl::-cp::--absolute-names::--::" + workspace
	executor.outputs[getfacl] = "user::rwx\ngroup::---\nother::---\n"
	executor.after = func(call string) {
		switch {
		case strings.HasPrefix(call, "install::-d::-m::0750::--::"):
			_ = os.Mkdir(workspace, 0750)
		case strings.HasPrefix(call, "setfacl::-m::u:10000:rwx,d:u:10000:rwx::--::"):
			executor.outputs[getfacl] = readyWorkspaceACL()
		}
	}
	var output bytes.Buffer
	prompter := newHostInstallPrompter(strings.NewReader(workspace+"\ny\ny\n"), &output, true)
	options := hostInstallOptions{StateRoot: stateRoot, ReleaseManifest: manifest}
	if err := resolveInstallWorkspace(t.Context(), &options, prompter, executor); err != nil {
		t.Fatal(err)
	}
	if options.Workspace != workspace {
		t.Fatalf("resolved workspace = %q", options.Workspace)
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		t.Fatalf("workspace was not created: %v", err)
	}
	if !strings.Contains(output.String(), "install -d -m 0750") ||
		!strings.Contains(output.String(), "setfacl -m u:10000:rwx,d:u:10000:rwx") {
		t.Fatalf("interactive onboarding did not preview exact changes: %s", output.String())
	}
	for _, call := range executor.calls {
		if strings.Contains(call, "chmod::-R") || strings.Contains(call, "chmod -R") {
			t.Fatalf("workspace onboarding used recursive chmod: %q", call)
		}
	}
}

func TestResolveInstallWorkspacePreservesExistingReadyWorkspace(t *testing.T) {
	workspace := t.TempDir()
	marker := filepath.Join(workspace, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	executor := &fakeHostExecutor{
		paths: map[string]bool{"getfacl": true, "setfacl": true},
		outputs: map[string]string{
			"getfacl::-cp::--absolute-names::--::" + workspace: readyWorkspaceACL(),
		},
		errs: map[string]error{},
	}
	options := hostInstallOptions{
		Workspace: workspace, StateRoot: filepath.Join(t.TempDir(), "state"),
		ReleaseManifest: filepath.Join(t.TempDir(), "release-manifest.json"),
	}
	if err := resolveInstallWorkspace(t.Context(), &options, nil, executor); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(marker)
	if err != nil || string(raw) != "keep" {
		t.Fatalf("existing workspace content changed: %q %v", raw, err)
	}
	for _, call := range executor.calls {
		if strings.HasPrefix(call, "setfacl::") || strings.HasPrefix(call, "install::") {
			t.Fatalf("ready workspace was mutated: %q", call)
		}
	}
}

func TestResolveInstallWorkspaceRejectsProtectedOverlapAndSymlink(t *testing.T) {
	stateRoot := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(stateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	options := hostInstallOptions{
		Workspace: filepath.Dir(stateRoot), StateRoot: stateRoot,
		ReleaseManifest: filepath.Join(t.TempDir(), "release-manifest.json"),
	}
	executor := &fakeHostExecutor{paths: map[string]bool{}, outputs: map[string]string{}, errs: map[string]error{}}
	if err := resolveInstallWorkspace(t.Context(), &options, nil, executor); err == nil ||
		!strings.Contains(err.Error(), "overlaps Loki host-management state") {
		t.Fatalf("protected overlap error = %v", err)
	}

	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "workspace")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	options = hostInstallOptions{
		Workspace: link, StateRoot: filepath.Join(t.TempDir(), "state"),
		ReleaseManifest: filepath.Join(t.TempDir(), "release-manifest.json"),
	}
	if err := resolveInstallWorkspace(t.Context(), &options, nil, executor); err == nil ||
		!strings.Contains(err.Error(), "real directory") {
		t.Fatalf("symlink workspace error = %v", err)
	}
}

func TestResolveInstallWorkspaceNonInteractiveMutationFlags(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	options := hostInstallOptions{
		Workspace: workspace, StateRoot: filepath.Join(t.TempDir(), "state"),
		ReleaseManifest: filepath.Join(t.TempDir(), "release-manifest.json"),
	}
	executor := &fakeHostExecutor{
		paths:   map[string]bool{"install": true, "getfacl": true, "setfacl": true},
		outputs: map[string]string{},
		errs:    map[string]error{},
	}
	if err := resolveInstallWorkspace(t.Context(), &options, nil, executor); err == nil ||
		!strings.Contains(err.Error(), "--create-workspace") {
		t.Fatalf("missing create approval error = %v", err)
	}

	options.CreateWorkspace = true
	executor.after = func(call string) {
		if strings.HasPrefix(call, "install::-d::-m::0750::--::") {
			_ = os.Mkdir(workspace, 0750)
		}
	}
	if err := resolveInstallWorkspace(t.Context(), &options, nil, executor); err == nil ||
		!strings.Contains(err.Error(), "--prepare-workspace") {
		t.Fatalf("missing ACL approval error = %v", err)
	}
}

func TestRunWorkspaceMutationRequiresExplicitSudoOutsideInteractiveMode(t *testing.T) {
	executor := &fakeHostExecutor{
		paths:   map[string]bool{"setfacl": true, "sudo": true},
		outputs: map[string]string{},
		errs: map[string]error{
			"setfacl::-m::u:10000:rwx::/workspace": errors.New("permission denied"),
		},
	}
	args := []string{"-m", "u:10000:rwx", "/workspace"}
	if err := runWorkspaceMutation(t.Context(), executor, false, nil, "setfacl", args, ""); err == nil ||
		!strings.Contains(err.Error(), "--allow-sudo-workspace") {
		t.Fatalf("missing sudo approval error = %v", err)
	}
	if err := runWorkspaceMutation(t.Context(), executor, true, nil, "setfacl", args, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(executor.calls, "\n"), "sudo::setfacl::-m::u:10000:rwx::/workspace") {
		t.Fatalf("approved sudo mutation was not used: %#v", executor.calls)
	}
}

func TestInteractiveDockerRuntimeOffersExplicitSudoWithoutDockerGroupMutation(t *testing.T) {
	executor := &fakeHostExecutor{
		paths: map[string]bool{"docker": true, "sudo": true},
		outputs: map[string]string{
			"sudo::docker::version::--format::{{.Server.Version}}": "29.8.1\n",
			"sudo::docker::compose::version::--short":              "5.5.1\n",
		},
		errs: map[string]error{
			"docker::version::--format::{{.Server.Version}}": errors.New("permission denied"),
		},
	}
	var output bytes.Buffer
	prompter := newHostInstallPrompter(strings.NewReader("y\n"), &output, true)
	options := hostInstallOptions{}
	probe, err := interactiveDockerRuntime(
		t.Context(), &options, runtimeRequirements(), prompter, executor,
	)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Access != hostDockerAccessSudo || !options.AllowSudoDocker {
		t.Fatalf("interactive sudo Docker probe = %#v options=%#v", probe, options)
	}
	body := output.String()
	if !strings.Contains(body, "will not add you to the docker group") ||
		!strings.Contains(body, "sudo docker compose version --short") {
		t.Fatalf("sudo Docker approval explanation = %q", body)
	}
	for _, call := range executor.calls {
		if strings.Contains(call, "usermod") || strings.Contains(call, "groupadd") {
			t.Fatalf("Docker group mutation was attempted: %q", call)
		}
	}
}

func TestApproveSudoDockerRuntimeWorksAfterPrerequisiteInstall(t *testing.T) {
	executor := &fakeHostExecutor{
		paths: map[string]bool{"docker": true, "sudo": true},
		outputs: map[string]string{
			"sudo::docker::version::--format::{{.Server.Version}}": "29.8.1\n",
			"sudo::docker::compose::version::--short":              "5.5.1\n",
		},
		errs: map[string]error{
			"docker::version::--format::{{.Server.Version}}": errors.New("permission denied"),
		},
	}
	prompter := newHostInstallPrompter(strings.NewReader("y\n"), &bytes.Buffer{}, true)
	options := hostInstallOptions{InstallPrerequisites: true}
	probe, err := approveSudoDockerRuntime(
		t.Context(), &options, runtimeRequirements(),
		releases.SupportedHost{Environment: "native", Distribution: "ubuntu", Version: "24.04", Arch: "amd64"},
		prompter, executor,
	)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Access != hostDockerAccessSudo || !options.AllowSudoDocker {
		t.Fatalf("post-install sudo Docker probe = %#v options=%#v", probe, options)
	}
}

func TestWorkspaceACLReadyRequiresEffectiveAccessAndInheritance(t *testing.T) {
	if !workspaceACLReady(readyWorkspaceACL()) {
		t.Fatal("ready ACL was rejected")
	}
	for _, raw := range []string{
		"user:10000:rwx\nmask::rwx\ndefault:user:10000:rwx\n",
		"user:10000:rwx\nmask::r-x\ndefault:user:10000:rwx\ndefault:mask::rwx\n",
		"user:10000:rwx\nmask::rwx\ndefault:user:10000:r-x\ndefault:mask::rwx\n",
	} {
		if workspaceACLReady(raw) {
			t.Fatalf("incomplete ACL was accepted: %q", raw)
		}
	}
}
