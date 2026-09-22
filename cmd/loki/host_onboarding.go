package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"loki/internal/host/releases"
)

const composeWorkspaceUID = 10000

type hostInstallPrompter struct {
	reader      *bufio.Reader
	writer      io.Writer
	interactive bool
}

func newHostInstallPrompter(input io.Reader, output io.Writer, interactive bool) *hostInstallPrompter {
	return &hostInstallPrompter{reader: bufio.NewReader(input), writer: output, interactive: interactive}
}

func defaultHostInstallPrompter(stdout io.Writer) *hostInstallPrompter {
	interactive := false
	if info, err := os.Stdin.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
		interactive = term.IsTerminal(int(os.Stdin.Fd()))
	}
	return newHostInstallPrompter(os.Stdin, stdout, interactive)
}

func (p *hostInstallPrompter) line(label string) (string, error) {
	if p == nil || !p.interactive {
		return "", errors.New("interactive input is unavailable")
	}
	if p.writer != nil {
		fmt.Fprint(p.writer, label)
	}
	line, err := p.reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errors.New("a value is required")
	}
	return line, nil
}

func (p *hostInstallPrompter) confirm(prompt string) (bool, error) {
	if p == nil || !p.interactive {
		return false, errors.New("interactive approval is unavailable")
	}
	if p.writer != nil {
		fmt.Fprintf(p.writer, "%s [y/N] ", prompt)
	}
	line, err := p.reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func resolveInstallWorkspace(
	ctx context.Context,
	options *hostInstallOptions,
	prompter *hostInstallPrompter,
	executor hostCommandExecutor,
) error {
	if options == nil {
		return errors.New("host install options are not configured")
	}
	if executor == nil {
		return errors.New("workspace onboarding executor is not configured")
	}

	workspace := strings.TrimSpace(options.Workspace)
	if workspace == "" {
		if prompter == nil || !prompter.interactive {
			return errors.New("--workspace is required for non-interactive installation")
		}
		var err error
		workspace, err = prompter.line("Workspace directory: ")
		if err != nil {
			return err
		}
	}
	clean, err := cleanInstallWorkspacePath(workspace)
	if err != nil {
		return err
	}
	if err = rejectProtectedWorkspace(clean, options.StateRoot, options.ReleaseManifest); err != nil {
		return err
	}
	options.Workspace = clean

	info, statErr := os.Lstat(clean)
	switch {
	case errors.Is(statErr, os.ErrNotExist):
		approved := options.CreateWorkspace
		command := "install -d -m 0750 -- " + shellQuote(clean)
		if !approved {
			if prompter == nil || !prompter.interactive {
				return errors.New("workspace does not exist; pass --create-workspace to approve creating it")
			}
			if prompter.writer != nil {
				fmt.Fprintf(prompter.writer, "Loki needs to create the workspace:\n  %s\n", command)
			}
			approved, err = prompter.confirm("Create this directory?")
			if err != nil {
				return err
			}
			if !approved {
				return errors.New("workspace creation was not approved")
			}
		}
		if err = runWorkspaceMutation(ctx, executor, options.AllowSudoWorkspace, prompter, "install",
			[]string{"-d", "-m", "0750", "--", clean},
			"Create the workspace with elevated privileges?",
		); err != nil {
			return err
		}
		info, statErr = os.Lstat(clean)
		if statErr != nil {
			return statErr
		}
	case statErr != nil:
		return statErr
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("workspace must be a real directory, not a symlink or special file")
	}

	return prepareWorkspaceACL(ctx, options, prompter, executor)
}

func cleanInstallWorkspacePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value ||
		value == string(filepath.Separator) || strings.ContainsRune(value, 0) ||
		strings.ContainsAny(value, "\r\n") {
		return "", errors.New("--workspace must be a clean absolute non-root path")
	}
	return value, nil
}

func rejectProtectedWorkspace(workspace, stateRoot, releaseManifest string) error {
	protected := []string{strings.TrimSpace(stateRoot)}
	if releaseManifest = strings.TrimSpace(releaseManifest); releaseManifest != "" {
		protected = append(protected, filepath.Dir(releaseManifest))
	}
	for _, root := range protected {
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		if pathsOverlap(workspace, root) {
			return errors.New("workspace overlaps Loki host-management state; choose a separate directory")
		}
	}
	return nil
}

func pathsOverlap(left, right string) bool {
	return pathContains(left, right) || pathContains(right, left)
}

func pathContains(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func prepareWorkspaceACL(
	ctx context.Context,
	options *hostInstallOptions,
	prompter *hostInstallPrompter,
	executor hostCommandExecutor,
) error {
	if options == nil {
		return errors.New("host install options are not configured")
	}
	if _, err := executor.LookPath("getfacl"); err != nil {
		if prepareErr := ensureACLPrerequisite(ctx, options, prompter, executor); prepareErr != nil {
			return prepareErr
		}
	}
	if _, err := executor.LookPath("setfacl"); err != nil {
		if prepareErr := ensureACLPrerequisite(ctx, options, prompter, executor); prepareErr != nil {
			return prepareErr
		}
	}

	raw, err := executor.Run(ctx, "getfacl", []string{"-cp", "--absolute-names", "--", options.Workspace}, "")
	if err == nil && workspaceACLReady(string(raw)) {
		return nil
	}

	command := fmt.Sprintf("setfacl -m u:%d:rwx,d:u:%d:rwx -- %s", composeWorkspaceUID, composeWorkspaceUID, shellQuote(options.Workspace))
	approved := options.PrepareWorkspace
	if !approved {
		if prompter == nil || !prompter.interactive {
			return errors.New("workspace needs Loki access; pass --prepare-workspace to approve the minimal POSIX ACL")
		}
		if prompter.writer != nil {
			fmt.Fprintf(prompter.writer, "Loki needs this minimal workspace ACL:\n  %s\n", command)
		}
		approved, err = prompter.confirm("Apply this workspace permission change?")
		if err != nil {
			return err
		}
		if !approved {
			return errors.New("workspace ACL preparation was not approved")
		}
	}
	if err = runWorkspaceMutation(ctx, executor, options.AllowSudoWorkspace, prompter, "setfacl",
		[]string{"-m", fmt.Sprintf("u:%d:rwx,d:u:%d:rwx", composeWorkspaceUID, composeWorkspaceUID), "--", options.Workspace},
		"Apply the workspace ACL with elevated privileges?",
	); err != nil {
		return err
	}
	raw, err = executor.Run(ctx, "getfacl", []string{"-cp", "--absolute-names", "--", options.Workspace}, "")
	if err != nil || !workspaceACLReady(string(raw)) {
		return errors.New("workspace ACL does not grant the required Loki runtime access")
	}
	return nil
}

func ensureACLPrerequisite(
	ctx context.Context,
	options *hostInstallOptions,
	prompter *hostInstallPrompter,
	executor hostCommandExecutor,
) error {
	host, err := releases.DetectHost()
	if err != nil {
		return errors.New("POSIX ACL tools are required; install setfacl/getfacl for this host")
	}
	if host.Distribution != "ubuntu" || host.Version != "24.04" {
		return errors.New("POSIX ACL tools are required; install the host's ACL package before continuing")
	}
	approved := options.InstallPrerequisites
	if !approved {
		if prompter == nil || !prompter.interactive {
			return errors.New("POSIX ACL tools are required; pass --install-prerequisites to approve installing the Ubuntu acl package")
		}
		if prompter.writer != nil {
			prefix := ""
			if os.Geteuid() != 0 {
				prefix = "sudo "
			}
			fmt.Fprintf(prompter.writer, "Loki needs the POSIX ACL tools:\n  %sapt-get install -y acl\n", prefix)
		}
		approved, err = prompter.confirm("Install the Ubuntu acl package?")
		if err != nil {
			return err
		}
		if !approved {
			return errors.New("POSIX ACL prerequisite installation was not approved")
		}
	}
	step := hostCommandStep{
		Description: "install the POSIX ACL tools", Executable: "apt-get",
		Args: []string{"install", "-y", "acl"}, Root: true,
	}
	if err = executeHostCommandPlan(ctx, []hostCommandStep{step}, executor); err != nil {
		return err
	}
	return nil
}

func workspaceACLReady(raw string) bool {
	var user, mask, defaultUser, defaultMask bool
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		switch line {
		case fmt.Sprintf("user:%d:rwx", composeWorkspaceUID):
			user = true
		case "mask::rwx":
			mask = true
		case fmt.Sprintf("default:user:%d:rwx", composeWorkspaceUID):
			defaultUser = true
		case "default:mask::rwx":
			defaultMask = true
		}
	}
	return user && mask && defaultUser && defaultMask
}

func runWorkspaceMutation(
	ctx context.Context,
	executor hostCommandExecutor,
	allowSudo bool,
	prompter *hostInstallPrompter,
	executable string,
	args []string,
	sudoPrompt string,
) error {
	if _, err := executor.LookPath(executable); err != nil {
		return fmt.Errorf("%s is required for workspace onboarding", executable)
	}
	if _, err := executor.Run(ctx, executable, args, ""); err == nil {
		return nil
	}
	if !allowSudo {
		if prompter == nil || !prompter.interactive {
			return fmt.Errorf("%s requires elevated privileges; pass --allow-sudo-workspace", executable)
		}
		if _, err := executor.LookPath("sudo"); err != nil {
			return fmt.Errorf("%s requires elevated privileges and sudo is unavailable", executable)
		}
		command := "sudo " + executable
		for _, arg := range args {
			command += " " + shellQuote(arg)
		}
		if prompter.writer != nil {
			fmt.Fprintf(prompter.writer, "Elevated workspace change required:\n  %s\n", command)
		}
		approved, err := prompter.confirm(sudoPrompt)
		if err != nil {
			return err
		}
		if !approved {
			return errors.New("elevated workspace change was not approved")
		}
	}
	if _, err := executor.LookPath("sudo"); err != nil {
		return errors.New("sudo is required for the approved workspace permission change")
	}
	sudoArgs := append([]string{executable}, args...)
	if _, err := executor.Run(ctx, "sudo", sudoArgs, ""); err != nil {
		return fmt.Errorf("approved elevated workspace change failed: %s", executable)
	}
	return nil
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func interactiveDockerRuntime(
	ctx context.Context,
	options *hostInstallOptions,
	release releases.RuntimeRequirements,
	prompter *hostInstallPrompter,
	executor hostCommandExecutor,
) (hostRuntimeProbe, error) {
	host := releases.SupportedHost{}
	if options.InstallPrerequisites {
		detected, err := releases.DetectHost()
		if err != nil {
			return hostRuntimeProbe{}, err
		}
		host = detected
	}
	probe, err := prepareHostDockerRuntime(
		ctx, release, host, options.InstallPrerequisites, options.AllowSudoDocker, executor, nil,
	)
	if err == nil || prompter == nil || !prompter.interactive {
		return probe, err
	}
	if errors.Is(err, errHostDockerSudoApprovalNeeded) {
		return approveSudoDockerRuntime(ctx, options, release, host, prompter, executor)
	}
	if !errors.Is(err, errHostPrerequisiteApprovalNeeded) {
		return hostRuntimeProbe{}, err
	}

	detected, detectErr := releases.DetectHost()
	if detectErr != nil {
		return hostRuntimeProbe{}, err
	}
	steps, planErr := ubuntuDockerPrerequisitePlan(detected)
	if planErr != nil {
		return hostRuntimeProbe{}, err
	}
	if prompter.writer != nil {
		fmt.Fprintln(prompter.writer, "Loki needs these host prerequisites:")
		for _, step := range steps {
			fmt.Fprintf(prompter.writer, "  - %s\n    %s\n", step.Description, formatHostCommand(step, os.Geteuid() == 0))
			if step.Stdin != "" {
				for _, line := range strings.Split(strings.TrimSuffix(step.Stdin, "\n"), "\n") {
					fmt.Fprintf(prompter.writer, "      %s\n", line)
				}
			}
		}
	}
	approved, promptErr := prompter.confirm("Install these prerequisites?")
	if promptErr != nil {
		return hostRuntimeProbe{}, promptErr
	}
	if !approved {
		return hostRuntimeProbe{}, errors.New("host prerequisite installation was not approved")
	}
	options.InstallPrerequisites = true
	host = detected
	probe, err = prepareHostDockerRuntime(
		ctx, release, host, true, options.AllowSudoDocker, executor, nil,
	)
	if err == nil {
		return probe, nil
	}
	if errors.Is(err, errHostDockerSudoApprovalNeeded) {
		return approveSudoDockerRuntime(ctx, options, release, host, prompter, executor)
	}
	return hostRuntimeProbe{}, err
}

func approveSudoDockerRuntime(
	ctx context.Context,
	options *hostInstallOptions,
	release releases.RuntimeRequirements,
	host releases.SupportedHost,
	prompter *hostInstallPrompter,
	executor hostCommandExecutor,
) (hostRuntimeProbe, error) {
	if options.AllowSudoDocker {
		return prepareHostDockerRuntime(ctx, release, host, options.InstallPrerequisites, true, executor, nil)
	}
	if prompter == nil || !prompter.interactive {
		return hostRuntimeProbe{}, fmt.Errorf("%w; pass --allow-sudo-docker", errHostDockerSudoApprovalNeeded)
	}
	if prompter.writer != nil {
		fmt.Fprintln(prompter.writer, "Docker is available, but the current user cannot access the daemon directly.")
		fmt.Fprintln(prompter.writer, "Loki will not add you to the docker group.")
		fmt.Fprintln(prompter.writer, "Host lifecycle operations can use the explicit sudo Docker boundary instead:")
		fmt.Fprintln(prompter.writer, "  sudo docker version --format '{{.Server.Version}}'")
		fmt.Fprintln(prompter.writer, "  sudo docker compose version --short")
	}
	approved, err := prompter.confirm("Allow sudo for Loki host lifecycle Docker commands?")
	if err != nil {
		return hostRuntimeProbe{}, err
	}
	if !approved {
		return hostRuntimeProbe{}, errors.New("sudo Docker access was not approved")
	}
	options.AllowSudoDocker = true
	return prepareHostDockerRuntime(ctx, release, host, options.InstallPrerequisites, true, executor, nil)
}
