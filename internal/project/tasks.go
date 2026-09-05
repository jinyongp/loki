package project

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"loki/internal/fault"
	"loki/internal/process"
	"loki/internal/state"
)

type TaskRequest struct {
	CWD        string         `json:"cwd"`
	Action     string         `json:"action"`
	Workstream *string        `json:"workstream"`
	UUID       *string        `json:"uuid"`
	Status     string         `json:"status"`
	Limit      *int           `json:"limit"`
	Offset     int            `json:"offset"`
	Fields     map[string]any `json:"fields"`
	Annotation *string        `json:"annotation"`
}

var (
	uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	tagPattern  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	datePattern = regexp.MustCompile(`^[\p{Nd}]{4}-[\p{Nd}]{2}-[\p{Nd}]{2}(?:T[\p{Nd}]{2}:[\p{Nd}]{2}:[\p{Nd}]{2}Z)?$`)
)

func TaskUUID(value any) (string, error) {
	v, ok := value.(string)
	if !ok {
		return "", fault.Error("a full task UUID is required")
	}
	if uuidPattern.MatchString(v) {
		return v, nil
	}
	plain := strings.ReplaceAll(strings.ReplaceAll(v, "urn:", ""), "uuid:", "")
	plain = strings.ReplaceAll(strings.Trim(plain, "{}"), "-", "")
	if len(plain) == 32 {
		if _, err := hex.DecodeString(plain); err == nil {
			return "", fault.Error("a canonical full task UUID is required")
		}
	}
	return "", fault.Error("a full task UUID is required")
}
func stringArray(value any) ([]string, bool) {
	if values, ok := value.([]string); ok {
		return values, true
	}
	values, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		s, ok := v.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}
func TaskAttributes(fields map[string]any) ([]string, error) {
	names := make([]string, 0, len(fields))
	for name := range fields {
		if !slices.Contains([]string{"description", "priority", "due", "wait", "scheduled", "tags", "depends"}, name) {
			return nil, fault.Error("unsupported task fields")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	out := []string{}
	for _, name := range names {
		value := fields[name]
		text, ok := value.(string)
		switch name {
		case "tags", "depends":
			values, arrayOK := stringArray(value)
			if !arrayOK {
				if raw, ok := value.([]any); ok && len(raw) <= 64 {
					for _, item := range raw {
						if name == "depends" {
							if _, err := TaskUUID(item); err != nil {
								return nil, err
							}
						} else if tag, ok := item.(string); !ok || !tagPattern.MatchString(tag) {
							return nil, fault.Error("invalid task tag")
						}
					}
				}
				return nil, fault.Error("task tags and dependencies must be bounded arrays")
			}
			if len(values) > 64 {
				return nil, fault.Error("task tags and dependencies must be bounded arrays")
			}
			for _, item := range values {
				if name == "depends" {
					if _, err := TaskUUID(item); err != nil {
						return nil, err
					}
				} else if !tagPattern.MatchString(item) {
					return nil, fault.Error("invalid task tag")
				}
			}
			out = append(out, name+":"+strings.Join(values, ","))
		case "description":
			if !ok || strings.TrimSpace(text) == "" || len(text) > 8192 || strings.ContainsRune(text, 0) {
				return nil, fault.Error("task description is empty or invalid")
			}
			out = append(out, "description:"+text)
		case "priority":
			if value != nil && (!ok || !slices.Contains([]string{"", "H", "M", "L"}, text)) {
				return nil, fault.Error("priority must be H, M, L, or null")
			}
			out = append(out, "priority:"+text)
		default:
			if value != nil && (!ok || !datePattern.MatchString(text)) {
				return nil, fault.Error("task dates must be ISO dates or null")
			}
			out = append(out, name+":"+text)
		}
	}
	return out, nil
}

type TaskRun func(context.Context, []string) (process.Result, error)

// OperateTask executes a single typed operation. The caller must hold the
// repository task.lock through every read, mutation, and verification read.
func OperateTask(ctx context.Context, run TaskRun, r TaskRequest, slug string, now time.Time) (map[string]any, error) {
	if !slices.Contains([]string{"list", "next", "get", "count", "add", "modify", "annotate", "start", "stop", "done", "delete"}, r.Action) {
		return nil, fault.Error("unsupported task action")
	}
	execute := func(args []string) (string, error) {
		args = append([]string{"rc.confirmation=off", "rc.verbose=nothing", "rc.json.array=on", "rc.hooks=off"}, args...)
		result, err := run(ctx, args)
		if err != nil {
			return "", err
		}
		if result.ExitCode != 0 {
			text := []rune(result.Output)
			return "", fault.Error("TASK_COMMAND_FAILED: " + string(text[max(0, len(text)-2048):]))
		}
		return result.Output, nil
	}
	read := func(identifier string) ([]map[string]any, error) {
		args := []string{"project.is:" + slug}
		if identifier != "" {
			if _, err := TaskUUID(identifier); err != nil {
				return nil, err
			}
			args = append(args, identifier)
		}
		text, err := execute(append(args, "export"))
		if err != nil {
			return nil, err
		}
		var rows []map[string]any
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.UseNumber()
		if !json.Valid([]byte(text)) || decoder.Decode(&rows) != nil || rows == nil {
			return nil, fault.Error("invalid Taskwarrior JSON response")
		}
		for _, row := range rows {
			if row == nil {
				return nil, fault.Error("invalid Taskwarrior JSON response")
			}
		}
		return rows, nil
	}
	identifier := ""
	if r.UUID != nil {
		identifier = *r.UUID
	}
	if slices.Contains([]string{"list", "next", "count"}, r.Action) {
		status := r.Status
		if status == "" {
			status = "pending"
		}
		if !slices.Contains([]string{"pending", "waiting", "completed", "deleted", "all"}, status) {
			return nil, fault.Error("invalid task status")
		}
		all, err := read("")
		if err != nil {
			return nil, err
		}
		items := []map[string]any{}
		for _, row := range all {
			if status == "all" || row["status"] == status {
				items = append(items, row)
			}
		}
		if r.Action == "next" {
			pending := map[string]bool{}
			for _, row := range all {
				if row["status"] == "pending" || row["status"] == "waiting" {
					uuid, _ := row["uuid"].(string)
					pending[uuid] = true
				}
			}
			stamp := now.UTC().Format("20060102T150405Z")
			ready := []map[string]any{}
			for _, row := range items {
				if row["status"] != "pending" {
					continue
				}
				blocked := false
				for _, name := range []string{"wait", "scheduled"} {
					if date, ok := row[name].(string); ok && date > stamp {
						blocked = true
					}
				}
				deps, _ := stringArray(row["depends"])
				for _, dep := range deps {
					if pending[dep] {
						blocked = true
					}
				}
				if !blocked {
					ready = append(ready, row)
				}
			}
			items = ready
			sort.SliceStable(items, func(i, j int) bool { return urgency(items[i]) > urgency(items[j]) })
		}
		limit := 50
		if r.Limit != nil {
			limit = *r.Limit
		}
		if limit < 1 || limit > 200 {
			return nil, fault.Error("task limit must be between 1 and 200")
		}
		if r.Offset < 0 {
			return nil, fault.Error("task offset must be a nonnegative integer")
		}
		start := min(r.Offset, len(items))
		end := start + min(limit, len(items)-start)
		page := items[start:end]
		if r.Action == "count" {
			page = []map[string]any{}
		}
		return map[string]any{"count": len(items), "tasks": page, "has_more": r.Action != "count" && end < len(items), "offset": r.Offset}, nil
	}
	if r.Action != "add" {
		if _, err := TaskUUID(identifier); err != nil {
			return nil, err
		}
		before, err := read(identifier)
		if err != nil {
			return nil, err
		}
		if len(before) != 1 {
			return nil, fault.Error("TASK_NOT_FOUND: UUID does not belong to this workstream")
		}
		if r.Action == "get" {
			return map[string]any{"task": before[0]}, nil
		}
	}
	switch r.Action {
	case "add", "modify":
		attrs, err := TaskAttributes(r.Fields)
		if err != nil {
			return nil, err
		}
		_, description := r.Fields["description"]
		if len(attrs) == 0 || r.Action == "add" && !description {
			return nil, fault.Error("task fields are required")
		}
		deps, _ := stringArray(r.Fields["depends"])
		for _, dep := range deps {
			rows, err := read(dep)
			if err != nil {
				return nil, err
			}
			if dep == identifier || len(rows) != 1 {
				return nil, fault.Error("dependency must belong to this workstream and differ from the task")
			}
		}
		if _, present := r.Fields["depends"]; r.Action == "modify" && present {
			rows, err := read("")
			if err != nil {
				return nil, err
			}
			graph := map[string][]string{}
			for _, row := range rows {
				id, _ := row["uuid"].(string)
				graph[id], _ = stringArray(row["depends"])
			}
			pending := slices.Clone(deps)
			visited := map[string]bool{}
			for len(pending) > 0 {
				current := pending[len(pending)-1]
				pending = pending[:len(pending)-1]
				if current == identifier {
					return nil, fault.Error("dependency cycle is not allowed")
				}
				if !visited[current] {
					visited[current] = true
					pending = append(pending, graph[current]...)
				}
			}
		}
		if r.Action == "add" {
			rows, err := read("")
			if err != nil {
				return nil, err
			}
			existing := map[string]bool{}
			for _, row := range rows {
				id, _ := row["uuid"].(string)
				existing[id] = true
			}
			if _, err = execute(append([]string{"add", "project:" + slug}, attrs...)); err != nil {
				return nil, err
			}
			rows, err = read("")
			if err != nil {
				return nil, err
			}
			created := []string{}
			for _, row := range rows {
				id, _ := row["uuid"].(string)
				if !existing[id] {
					created = append(created, id)
				}
			}
			if len(created) != 1 {
				return nil, fault.Error("task creation readback is ambiguous; inspect before retrying")
			}
			identifier = created[0]
		} else {
			if _, err = execute(append([]string{identifier, "modify"}, attrs...)); err != nil {
				return nil, err
			}
		}
	case "annotate":
		var note any
		if r.Annotation != nil {
			note = *r.Annotation
		}
		if _, err := TaskAttributes(map[string]any{"description": note}); err != nil {
			return nil, err
		}
		if _, err := execute([]string{identifier, "annotate", "--", *r.Annotation}); err != nil {
			return nil, err
		}
	default:
		if _, err := execute([]string{identifier, r.Action}); err != nil {
			return nil, err
		}
	}
	after, err := read(identifier)
	if err != nil {
		return nil, err
	}
	if len(after) != 1 {
		return nil, fault.Error("task mutation readback failed")
	}
	return map[string]any{"task": after[0], "action": r.Action}, nil
}
func urgency(row map[string]any) float64 {
	value, _ := strconv.ParseFloat(fmt.Sprint(row["urgency"]), 64)
	return value
}

type Tasks struct {
	Store  *Store
	Binary string
	Home   string
	Now    func() time.Time
}

func (t Tasks) run(ctx context.Context, id Identity, args []string, timeout time.Duration) (process.Result, error) {
	home := t.Home
	if home == "" {
		home = "/home/runner"
	}
	env := []string{"HOME=" + home, "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "PATH=/home/linuxbrew/.linuxbrew/bin:/usr/bin:/bin", "TASKRC=" + filepath.Join(id.StateDirectory, "taskwarrior", "taskrc"), "TASKDATA=" + filepath.Join(id.StateDirectory, "taskwarrior", "data")}
	argv := append([]string{t.Binary}, args...)
	if t.Store.Runner != "" {
		argv = t.Store.asRunner(append(append([]string{"/usr/bin/env", "-i"}, env...), argv...))
	}
	result, err := process.Run(ctx, process.Spec{Argv: argv, CWD: t.Store.WorkspaceRoot, Env: env, Timeout: timeout, MaxOutput: 1_048_576})
	if err == nil && result.Truncated {
		err = fault.Error("TASK_OUTPUT_LIMIT: narrow the queue before retrying")
	}
	return result, err
}

func (t Tasks) Do(ctx context.Context, r TaskRequest) (map[string]any, error) {
	id, err := t.Store.Resolve(ctx, r.CWD)
	if err != nil {
		return nil, err
	}
	out, err := t.Store.StatusIdentity(id)
	if err != nil {
		return nil, err
	}
	if r.Action == "status" || r.Action == "diagnostics" {
		return t.health(ctx, id, out)
	}
	if !out["initialized"].(bool) {
		return nil, fault.Error("TASK_NOT_INITIALIZED: call project action=init for this repository")
	}
	slug := ""
	if r.Workstream != nil {
		slug = *r.Workstream
	}
	if slug == "" {
		slug, _ = out["active_workstream"].(string)
	}
	if slug == "" {
		return nil, fault.Error("TASK_WORKSTREAM_REQUIRED: select or bind a workstream")
	}
	if _, err = t.Store.Workstream(id, slug); err != nil {
		if err == fault.Error("unknown workstream") {
			return nil, fault.Error("TASK_WORKSTREAM_UNKNOWN")
		}
		return nil, err
	}
	if err = t.checkData(id); err != nil {
		return nil, err
	}
	release, err := state.LockFile(ctx, filepath.Join(id.StateDirectory, "task.lock"))
	if err != nil {
		return nil, err
	}
	defer release()
	now := time.Now()
	if t.Now != nil {
		now = t.Now()
	}
	result, err := OperateTask(ctx, func(ctx context.Context, args []string) (process.Result, error) {
		return t.run(ctx, id, args, 30*time.Second)
	}, r, slug, now)
	if err != nil {
		return nil, err
	}
	out["workstream"] = slug
	for k, v := range result {
		out[k] = v
	}
	return out, nil
}
func (t Tasks) checkData(id Identity) error {
	_, err := readFile(filepath.Join(id.StateDirectory, "taskwarrior", "taskrc"), 65536)
	if err == nil {
		var info os.FileInfo
		info, err = os.Lstat(filepath.Join(id.StateDirectory, "taskwarrior", "data"))
		if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return fault.Error("central project state path is unsafe")
		}
	}
	if errors.Is(err, os.ErrPermission) {
		return fault.Error("TASK_PERMISSION_DENIED: central metadata requires administrator repair")
	}
	if errors.Is(err, os.ErrNotExist) {
		return fault.Error("TASK_NOT_INITIALIZED")
	}
	return err
}
func (t Tasks) health(ctx context.Context, id Identity, out map[string]any) (map[string]any, error) {
	version, err := process.Run(ctx, process.Spec{Argv: []string{t.Binary, "--version"}, Env: []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "TASKRC=/dev/null"}, Timeout: 10 * time.Second, MaxOutput: 4096})
	if err != nil {
		return nil, err
	}
	health := map[string]any{"initialized": out["initialized"], "metadata_readable": false, "runner_access": false}
	if out["initialized"].(bool) {
		taskroot := filepath.Join(id.StateDirectory, "taskwarrior")
		_, err := readFile(filepath.Join(taskroot, "taskrc"), 65536)
		health["metadata_readable"] = err == nil
		access := true
		for _, probe := range [][]string{{"-r", filepath.Join(taskroot, "taskrc")}, {"-w", filepath.Join(taskroot, "data")}} {
			result, err := process.Run(ctx, process.Spec{Argv: t.Store.asRunner(append([]string{"/usr/bin/test"}, probe...)), Timeout: 10 * time.Second, MaxOutput: 4096})
			access = access && err == nil && result.ExitCode == 0
		}
		health["runner_access"] = access
	}
	out["version"] = strings.TrimSpace(version.Output)
	out["available"] = version.ExitCode == 0
	out["health"] = health
	return out, nil
}
