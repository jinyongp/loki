package workspace

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"loki/internal/fault"
	"loki/internal/gitops"
	"loki/internal/policy"
	"loki/internal/process"
)

var patchDenied = []string{"GIT binary patch", "Binary files ", "rename from ", "rename to ", "copy from ", "copy to ", "deleted file mode ", "new file mode 120000", "old file mode 120000", "new mode 120000", "old mode 120000", "+++ /dev/null"}

func (f *Files) Search(ctx context.Context, query, path string, limit int, regex bool) (map[string]any, error) {
	if query == "" || utf8.RuneCountInString(query) > 500 {
		return nil, fault.Error("invalid request: query length must be between 1 and 500 characters")
	}
	if _, err := f.Policy.Resolve(path, true); err != nil {
		return nil, err
	}
	relative, _ := policy.Relative(path)
	limit = clamp(limit, 1, f.Config.MaxSearchResults)
	argv := []string{f.RGPath, "--json", "--line-number", "--column", "--hidden", "--no-config", "--no-messages", "--max-filesize", strconv.Itoa(f.Config.MaxFileBytes)}
	if !regex {
		argv = append(argv, "--fixed-strings")
	}
	for _, glob := range []string{"!.git/**", "!.ssh/**", "!.gnupg/**", "!.env", "!.env.local", "!.env.production", "!*.pem", "!*.key", "!*.p12", "!*.pfx"} {
		argv = append(argv, "--glob", glob)
	}
	argv = append(argv, "--", query, relative)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := process.Command(ctx, process.Spec{Argv: argv, CWD: f.Policy.Root()})
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 65536), max(f.Config.MaxFileBytes*6, 65536))
	matches := []map[string]any{}
	truncated := false
	for scanner.Scan() {
		var event struct {
			Type string `json:"type"`
			Data struct {
				Path struct {
					Text string `json:"text"`
				} `json:"path"`
				Lines struct {
					Text string `json:"text"`
				} `json:"lines"`
				Line       int `json:"line_number"`
				Submatches []struct {
					Start int `json:"start"`
				} `json:"submatches"`
			} `json:"data"`
		}
		if err = json.Unmarshal(scanner.Bytes(), &event); err != nil {
			cmd.Process.Kill()
			break
		}
		if event.Type != "match" {
			continue
		}
		matched := strings.TrimPrefix(event.Data.Path.Text, "./")
		if _, err := policy.Relative(matched); err != nil {
			continue
		}
		column := 1
		if len(event.Data.Submatches) > 0 {
			column = event.Data.Submatches[0].Start + 1
		}
		text := []rune(strings.TrimRight(event.Data.Lines.Text, "\r\n"))
		if len(text) > 2000 {
			text = text[:2000]
		}
		matches = append(matches, map[string]any{"path": matched, "line": event.Data.Line, "column": column, "text": string(text)})
		if len(matches) >= limit {
			truncated = true
			cmd.Process.Kill()
			break
		}
	}
	stdout.Close()
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil, fault.Error("search timed out")
	}
	if err != nil {
		return nil, fault.Error("text search returned invalid data")
	}
	if scanner.Err() != nil && !truncated {
		return nil, fault.Error("text search exceeded output limits")
	}
	if waitErr != nil && !truncated && cmd.ProcessState.ExitCode() != 1 {
		return nil, fault.Error(fmt.Sprintf("text search failed with ripgrep exit code %d; narrow the path or run diagnostics", cmd.ProcessState.ExitCode()))
	}
	return map[string]any{"matches": matches, "truncated": truncated}, nil
}

type PatchFile struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
}

func parseNumstat(data []byte) ([]PatchFile, error) {
	files := []PatchFile{}
	for _, record := range bytes.Split(data, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		fields := bytes.SplitN(record, []byte{'\t'}, 3)
		if len(fields) != 3 {
			return nil, fault.Error("invalid request: unable to parse patch file list")
		}
		if string(fields[0]) == "-" || string(fields[1]) == "-" {
			return nil, fault.Error("binary patches are not allowed")
		}
		if !utf8.Valid(fields[2]) {
			return nil, fault.Error("patch paths must be UTF-8")
		}
		added, err := strconv.Atoi(string(fields[0]))
		if err != nil {
			return nil, err
		}
		deleted, err := strconv.Atoi(string(fields[1]))
		if err != nil {
			return nil, err
		}
		files = append(files, PatchFile{string(fields[2]), added, deleted})
	}
	return files, nil
}

func (f *Files) git(ctx context.Context, args []string, input []byte, timeout time.Duration) (gitops.CommandResult, error) {
	if f.GitRunner == nil {
		return gitops.CommandResult{}, errors.New("workspace Git runner is not configured")
	}
	prefix := []string{
		"/usr/bin/git", "--no-pager", "--literal-pathspecs",
		"-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=/dev/null",
	}
	return f.GitRunner.Run(ctx, gitops.CommandRequest{
		Argv: append(prefix, args...), CWD: ".", Input: input,
		Timeout: timeout, MaxOutput: f.Config.MaxOutputBytes,
	})
}

func (f *Files) Patch(ctx context.Context, patch string) (map[string]any, error) {
	if patch == "" || len(patch) > f.Config.MaxPatchBytes {
		return nil, fault.Error("patch is empty or exceeds the patch limit")
	}
	if strings.ContainsRune(patch, 0) {
		return nil, fault.Error("binary, delete, rename, copy, and symlink patches are not allowed")
	}
	for _, denied := range patchDenied {
		if strings.Contains(patch, denied) {
			return nil, fault.Error("binary, delete, rename, copy, and symlink patches are not allowed")
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.reconcileBatchesLocked(); err != nil {
		return nil, err
	}
	encoded := []byte(patch)
	numstat, err := f.git(ctx, []string{"apply", "--numstat", "-z"}, encoded, 15*time.Second)
	if err != nil {
		return nil, err
	}
	if numstat.ExitCode != 0 {
		return nil, fault.Error("invalid request: invalid patch: " + numstat.Output)
	}
	if numstat.Truncated {
		return nil, fault.Error("patch file list exceeds output limit")
	}
	files, err := parseNumstat(numstat.Raw)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fault.Error("invalid request: patch contains no file changes")
	}
	if len(files) > f.Config.MaxPatchFiles {
		return nil, fault.Error("patch changes too many files")
	}
	for _, file := range files {
		if _, err = f.Policy.Resolve(file.Path, false); err != nil {
			return nil, err
		}
	}
	checked, err := f.git(ctx, []string{"apply", "--check"}, encoded, 30*time.Second)
	if err != nil {
		return nil, err
	}
	if checked.ExitCode != 0 {
		return nil, fault.Error("invalid request: patch check failed: " + checked.Output)
	}
	revisions := map[string]string{}
	for _, file := range files {
		data, info, err := f.read(file.Path, 64<<20)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		revision, err := f.capture(file.Path, "apply_patch", data, info.Mode())
		if err != nil {
			return nil, err
		}
		revisions[file.Path] = revision
	}
	applied, err := f.git(ctx, []string{"apply"}, encoded, 30*time.Second)
	if err != nil {
		return nil, err
	}
	if applied.ExitCode != 0 {
		return nil, fault.Error("invalid request: patch apply failed: " + applied.Output)
	}
	return map[string]any{"files": files, "patch_sha256": Digest(encoded), "warnings": applied.Output, "previous_revisions": revisions}, nil
}

func (f *Files) RemoveTracked(ctx context.Context, path, expected string) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.reconcileBatchesLocked(); err != nil {
		return nil, err
	}
	data, info, err := f.read(path, 64<<20)
	if err != nil {
		return nil, err
	}
	digest := Digest(data)
	if digest != expected {
		return nil, fault.New(fault.CodeConflict, "file changed since it was read; read it again before removing", false, "read the file again and use its current sha256")
	}
	tracked, err := (&gitops.Controller{Paths: f.Policy, Config: f.Config, Runner: f.GitRunner}).TrackedFile(ctx, path)
	if err != nil {
		return nil, err
	}
	if !tracked {
		return nil, fault.Error("only Git-tracked files may be removed; move unwanted untracked files into the repository's ignored .tmp/loki-quarantine/ directory with move_path")
	}
	revision, err := f.capture(path, "remove_tracked_file", data, info.Mode())
	if err != nil {
		return nil, err
	}
	if err = f.Policy.RemoveFile(path); err != nil {
		return nil, err
	}
	return map[string]any{"path": path, "removed": true, "previous_revision": revision, "previous_sha256": digest}, nil
}
