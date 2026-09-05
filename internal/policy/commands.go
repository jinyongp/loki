package policy

import (
	"maps"
	"slices"
	"strings"

	"loki/internal/fault"
)

// ExecutablePath is the fixed built-in executable registry. Configuration may
// add workspace-command entries; registered secret actions use only this list.
var executables = map[string]string{
	"actionlint": "/home/linuxbrew/.linuxbrew/bin/actionlint", "actions-up": "/home/linuxbrew/.linuxbrew/bin/actions-up",
	"awk": "/usr/bin/awk", "cargo": "/workspace/.loki/cargo/bin/cargo", "cp": "/usr/bin/cp", "find": "/usr/bin/find",
	"fd": "/home/linuxbrew/.linuxbrew/bin/fd", "fnm": "/home/linuxbrew/.linuxbrew/bin/fnm", "gh": "/home/linuxbrew/.linuxbrew/bin/gh",
	"git": "/usr/bin/git", "go": "/home/linuxbrew/.linuxbrew/bin/go", "grep": "/usr/bin/grep", "head": "/usr/bin/head",
	"hyperfine": "/home/linuxbrew/.linuxbrew/bin/hyperfine", "just": "/home/linuxbrew/.linuxbrew/bin/just", "jq": "/usr/bin/jq",
	"ls": "/usr/bin/ls", "mkdir": "/usr/bin/mkdir", "mv": "/usr/bin/mv", "node": "node", "npm": "npm", "pnpm": "pnpm",
	"printenv": "/usr/bin/printenv", "pwd": "/usr/bin/pwd", "python": "/usr/bin/python3", "python3": "/usr/bin/python3",
	"rg": "/usr/bin/rg", "rustc": "/workspace/.loki/cargo/bin/rustc", "rustdoc": "/workspace/.loki/cargo/bin/rustdoc",
	"rustfmt": "/workspace/.loki/cargo/bin/rustfmt", "rustup": "/home/linuxbrew/.linuxbrew/bin/rustup", "sed": "/usr/bin/sed",
	"sort": "/usr/bin/sort", "tail": "/usr/bin/tail", "task": "/usr/local/libexec/loki-task", "tar": "/usr/bin/tar",
	"touch": "/usr/bin/touch", "uniq": "/usr/bin/uniq", "wc": "/usr/bin/wc",
}

func ExecutablePath(name string) (string, bool) { path, ok := executables[name]; return path, ok }
func ExecutableNames() []string                 { return slices.Sorted(maps.Keys(executables)) }

func ValidateExec(name string, args []string) error {
	lower := make([]string, len(args))
	for i, v := range args {
		lower[i] = strings.ToLower(v)
	}
	has := func(values ...string) bool {
		for _, arg := range lower {
			if slices.Contains(values, arg) {
				return true
			}
		}
		return false
	}
	prefix := func(values ...string) bool {
		for _, arg := range lower {
			for _, value := range values {
				if strings.HasPrefix(arg, value) {
					return true
				}
			}
		}
		return false
	}
	first := ""
	if len(lower) > 0 {
		first = lower[0]
	}
	switch name {
	case "node":
		if has("-e", "--eval", "-p", "--print") {
			return fault.Error("inline Node execution is not allowed")
		}
	case "python", "python3":
		if has("-c", "-m") {
			return fault.Error("inline or module Python execution is not allowed")
		}
	case "go":
		if len(lower) >= 2 && first == "env" && slices.Contains([]string{"-w", "-u"}, lower[1]) {
			return fault.Error("persistent Go environment changes are not allowed")
		}
	case "rustup":
		if first == "run" || first == "self" {
			return fault.Error("Rustup command bypass or self-modification is not allowed")
		}
	case "find":
		if has("-delete", "-exec", "-execdir", "-ok", "-okdir") {
			return fault.Error("destructive find actions are not allowed")
		}
	case "fd":
		if has("-x", "--exec", "--exec-batch") || prefix("--exec=", "--exec-batch=") {
			return fault.Error("fd external command execution is not allowed")
		}
	case "actionlint":
		if has("-shellcheck", "-pyflakes") || prefix("-shellcheck=", "-pyflakes=") {
			return fault.Error("actionlint external command overrides are not allowed")
		}
	case "hyperfine":
		return validateHyperfine(args)
	case "task":
		if has("config", "execute", "import", "purge", "sync", "synchronize", "undo") || prefix("rc.") {
			return fault.Error("Taskwarrior configuration, hooks, bulk import, sync, and command execution are not allowed")
		}
	case "git":
		if has("-c", "--git-dir", "--work-tree", "--namespace", "--super-prefix") || prefix("--config-env", "--exec-path", "--git-dir=", "--work-tree=", "--namespace=", "--super-prefix=") {
			return fault.Error("Git command injection options are not allowed")
		}
		sub := ""
		for _, arg := range lower {
			if !strings.HasPrefix(arg, "-") {
				sub = arg
				break
			}
		}
		if has("--no-gpg-sign", "--no-sign") {
			return fault.Error("unsigned Git commits and tags are not allowed")
		}
		if slices.Contains([]string{"commit-tree", "fast-import", "mktag"}, sub) {
			return fault.Error("Git object creation that bypasses signing is not allowed")
		}
		if slices.Contains([]string{"clean", "reset", "restore"}, sub) {
			return fault.Error("destructive Git subcommand is not allowed")
		}
		if sub == "checkout" && has("--") {
			return fault.Error("destructive Git checkout is not allowed")
		}
		if sub == "config" && !AllowedGitConfigRead(args) {
			return fault.Error("only allowlisted Git configuration reads are allowed")
		}
		if sub == "push" && (has("-f", "--force", "--force-with-lease", "--mirror", "--delete") || prefix("--force=")) {
			return fault.Error("destructive Git push options are not allowed")
		}
		if sub == "submodule" && has("foreach") {
			return fault.Error("Git submodule foreach is not allowed")
		}
	case "gh":
		if first == "api" {
			return fault.Error("arbitrary GitHub API calls are not allowed")
		}
		if first == "auth" && (len(lower) < 2 || lower[1] != "status") {
			return fault.Error("only GitHub auth status is allowed")
		}
		if first == "secret" || first == "variable" {
			return fault.Error("GitHub secret and variable access is not allowed")
		}
		if len(lower) >= 2 && (first == "repo" && slices.Contains([]string{"delete", "archive", "rename"}, lower[1]) || first == "release" && lower[1] == "delete") {
			return fault.Error("destructive GitHub operation is not allowed")
		}
	}
	return nil
}

func AllowedGitConfigRead(args []string) bool {
	if len(args) < 2 || strings.ToLower(args[0]) != "config" {
		return false
	}
	if !slices.Contains([]string{"commit.template", "commit.gpgsign", "gpg.format", "user.name", "user.email"}, strings.ToLower(args[len(args)-1])) {
		return false
	}
	gets := 0
	for _, arg := range args[1 : len(args)-1] {
		arg = strings.ToLower(arg)
		if !slices.Contains([]string{"--global", "--local", "--system", "--includes", "--show-origin", "--path", "--get", "--get-all"}, arg) {
			return false
		}
		if arg == "--get" || arg == "--get-all" {
			gets++
		}
	}
	return gets == 1
}

func validateHyperfine(args []string) error {
	if len(args) == 1 && slices.Contains([]string{"-h", "--help", "-V", "--version"}, args[0]) {
		return nil
	}
	values := []string{"-w", "--warmup", "-m", "--min-runs", "-M", "--max-runs", "-r", "--runs", "--style", "--sort", "-u", "--time-unit", "--export-asciidoc", "--export-csv", "--export-json", "--export-markdown", "--export-orgmode", "--output", "--input", "-n", "--command-name"}
	commands := []string{}
	shellDisabled := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-N" || arg == "--shell=none":
			shellDisabled = true
		case arg == "--ignore-failure" || arg == "--show-output" || strings.HasPrefix(arg, "--ignore-failure="):
		case slices.Contains(values, arg):
			if i+1 == len(args) {
				return fault.Error("hyperfine option " + arg + " requires a value")
			}
			i++
		case strings.HasPrefix(arg, "--") && slices.Contains(values, strings.SplitN(arg, "=", 2)[0]) && strings.Contains(arg, "="):
		case strings.HasPrefix(arg, "-"):
			return fault.Error("hyperfine option is not allowed")
		default:
			commands = append(commands, arg)
		}
	}
	if !shellDisabled {
		return fault.Error("hyperfine requires --shell=none")
	}
	if len(commands) == 0 {
		return fault.Error("hyperfine requires at least one benchmark command")
	}
	for _, text := range commands {
		argv, err := splitShellWords(text)
		if err != nil {
			return err
		}
		if len(argv) == 0 || !slices.Contains([]string{"actionlint", "cargo", "fd", "git", "go", "jq", "just", "rg", "rustc", "rustdoc", "rustfmt"}, argv[0]) {
			return fault.Error("hyperfine benchmark executable is not allowed")
		}
		if err = ValidateExec(argv[0], argv[1:]); err != nil {
			return err
		}
	}
	return nil
}

// splitShellWords matches shlex.split's POSIX quoting, without invoking a shell.
// Only hyperfine's --shell=none benchmark argv is accepted by the caller.
func splitShellWords(text string) ([]string, error) {
	words := []string{}
	var word strings.Builder
	var quote byte
	started := false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if quote == '\'' {
			if c == '\'' {
				quote = 0
			} else {
				word.WriteByte(c)
			}
			continue
		}
		if c == '\\' {
			if i+1 == len(text) {
				return nil, fault.Error("invalid hyperfine benchmark command")
			}
			i++
			next := text[i]
			if quote == '"' && next != '"' && next != '\\' {
				word.WriteByte('\\')
			}
			word.WriteByte(next)
			started = true
			continue
		}
		if quote == '"' {
			if c == '"' {
				quote = 0
			} else {
				word.WriteByte(c)
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			started = true
			continue
		}
		if strings.ContainsRune(" \t\r\n", rune(c)) {
			if started {
				words = append(words, word.String())
				word.Reset()
				started = false
			}
			continue
		}
		started = true
		word.WriteByte(c)
	}
	if quote != 0 {
		return nil, fault.Error("invalid hyperfine benchmark command")
	}
	if started {
		words = append(words, word.String())
	}
	return words, nil
}
