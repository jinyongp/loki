//go:build linux

package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	controlpolicy "loki/internal/control/policy"
	"loki/internal/platform/privateio"
	"loki/internal/platform/repository"
	"loki/internal/secretoutput"
	"loki/modules/execution/jobs"
	"loki/tools/acceptance/mcpclient"
)

func prepareAuthenticatedJobWorkspace(t *testing.T, root string) string {
	t.Helper()
	for _, name := range []string{".git", ".git/objects", ".git/refs"} {
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"HEAD": "ref: refs/heads/main\n", "config": "[core]\nrepositoryformatversion = 0\nbare = false\n"} {
		path := filepath.Join(root, ".git", name)
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := repository.New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	id, err := r.Identity(".")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type capacityValue struct {
	name, kind string
	body       []byte
}

func maximumCapacityKeyKinds() map[string]string {
	kinds := map[string]string{"TOKEN": "secret", "API_URL": "variable", "MAXA": "secret", "MAXB": "secret", "MAXC": "secret"}
	for i := 0; i < 56; i++ {
		kinds[fmt.Sprintf("S%03d", i)] = "secret"
	}
	for i := 0; len(kinds) < controlpolicy.MaxApplicationKeys; i++ {
		kinds[fmt.Sprintf("PAD%03d", i)] = "secret"
	}
	return kinds
}

func maximumCapacityValues(t *testing.T, base map[string]string, argv []string) ([]capacityValue, [][]byte) {
	t.Helper()
	values := map[string][]byte{"TOKEN": []byte("PRIVATE"), "API_URL": []byte("public-capacity")}
	// Raw '&' and its JSON HTML form have disjoint 10,000/60,000-byte
	// prefixes. Suffixes !000.. !055 add exactly 64 trie states to each.
	for i := 0; i < 56; i++ {
		values[fmt.Sprintf("S%03d", i)] = []byte(strings.Repeat("&", 10000) + fmt.Sprintf("!%03d", i))
	}
	remainingStates := secretoutput.MaxStates - 1 - len(values["TOKEN"]) - (10000 + 60000 + 2*(1+1+6+56))
	for i, name := range []string{"MAXA", "MAXB", "MAXC"} {
		n := remainingStates / 3
		if i < remainingStates%3 {
			n++
		}
		values[name] = bytes.Repeat([]byte{byte('a' + i)}, n)
	}
	kinds := maximumCapacityKeyKinds()
	var pads []string
	for name := range kinds {
		if _, found := values[name]; !found {
			values[name] = []byte{}
			pads = append(pads, name)
		}
	}
	sort.Strings(pads)
	used := (len(argv) + len(base) + len(values) + 2) * 8
	for _, arg := range argv {
		used += len(arg) + 1
	}
	for name, value := range base {
		used += len(name) + len(value) + 2
	}
	for name, value := range values {
		used += len(name) + len(value) + 2
	}
	remaining := (8<<20)/4 - used
	if len(pads) < 3 || remaining < 1 {
		t.Fatal("invalid accepted capacity fixture")
	}
	// The shared 'p' chain adds L states. Shortening MAXA by L preserves
	// the exact state bound while spending every remaining exec byte on pads.
	length := (remaining + len(pads) - 2) / (len(pads) - 1)
	tail := remaining - (len(pads)-2)*length
	if tail < 1 || tail > length || length >= len(values["MAXA"]) {
		t.Fatal("invalid simultaneous capacity limits")
	}
	old := values["MAXA"]
	values["MAXA"] = append([]byte(nil), old[:len(old)-length]...)
	clear(old)
	for i, name := range pads {
		n := length
		if i == len(pads)-1 {
			n = tail
		}
		values[name] = bytes.Repeat([]byte{'p'}, n)
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	var result []capacityValue
	var patterns [][]byte
	input := privateio.Bootstrap{Protocol: 1, Environment: []privateio.EnvironmentEntry{}}
	for name, value := range base {
		input.Environment = append(input.Environment, privateio.EnvironmentEntry{Name: name, Value: value})
	}
	for _, name := range names {
		result = append(result, capacityValue{name, kinds[name], values[name]})
		input.Environment = append(input.Environment, privateio.EnvironmentEntry{Name: name, Value: string(values[name])})
		if kinds[name] == "secret" {
			patterns = append(patterns, values[name])
		}
	}
	if len(result) != controlpolicy.MaxApplicationKeys {
		t.Fatal("capacity fixture lost granted keys")
	}
	env, err := privateio.ValidateBootstrap(input, argv, 8<<20, 4096)
	clear(env)
	if err != nil {
		t.Fatal("maximum accepted exec environment", err)
	}
	input.Environment[len(input.Environment)-1].Value += "p"
	if env, err := privateio.ValidateBootstrap(input, argv, 8<<20, 4096); err == nil {
		clear(env)
		t.Fatal("one-byte-over exec environment accepted")
	}
	return result, patterns
}

func TestSimultaneousMaximumCapacityFixture(t *testing.T) {
	values, patterns := maximumCapacityValues(t, map[string]string{"PATH": "/usr/bin:/bin"}, []string{"/bin/true"})
	defer func() {
		for _, value := range values {
			clear(value.body)
		}
	}()
	matcher, err := secretoutput.New(patterns)
	if err != nil {
		t.Fatal("accepted simultaneous limits", err)
	}
	matcher.Destroy()
	if matcher, err := secretoutput.New(append(patterns, []byte("z"))); err == nil {
		matcher.Destroy()
		t.Fatal("maximum trie budget did not reject one additional state")
	}
}

func verifyRealComposeAuthenticatedJobs(t *testing.T, ctx context.Context, b *Backend, state runtimeState, passphrase []byte) {
	t.Helper()
	fixtureApplicationAdmin(t, ctx, b, state, passphrase, "protection", "unlock", "--stdin")
	token, err := os.ReadFile(b.tokenPath())
	if err != nil {
		t.Fatal(err)
	}
	defer clear(token)
	endpoint := "http://127.0.0.1:" + strconv.Itoa(state.effectiveMCPPort()) + "/mcp"
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal("actual MCP authentication boundary", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal("unauthenticated MCP accepted")
	}
	connect := func() *mcpclient.Session {
		t.Helper()
		client, err := mcpclient.New(ctx, endpoint, token)
		if err != nil {
			t.Fatal("actual authenticated MCP connection", err)
		}
		return client
	}
	client := connect()
	defer func() { client.Close() }()
	call := func(name string, args map[string]any) (map[string]any, error) {
		return client.Call(ctx, name, args)
	}
	must := func(name string, args map[string]any) map[string]any {
		t.Helper()
		value, err := call(name, args)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	if _, err := call("secret_inspect", map[string]any{"action": "status", "resource": "ungranted"}); err == nil {
		t.Fatal("ungranted application resource accepted")
	}
	values := must("secret_inspect", map[string]any{"action": "values", "resource": "fixture"})
	// Migration already populated TOKEN. Generation deliberately creates an
	// absent granted key; replacing an existing secret is an admin operation.
	generate := map[string]any{"action": "generate", "resource": "fixture", "name": "MAXA", "expected_revision": values["revision"], "request_id": privateio.OperationID(b.root, "mcp", "generate")}
	generated := must("secret_write", generate)
	if generated["bytes"] != float64(32) {
		t.Fatal("MCP generation length changed")
	}
	client.Close()
	client = connect()
	replayed := must("secret_write", generate)
	if replayed["replayed"] != true || replayed["revision"] != generated["revision"] {
		t.Fatal("new MCP session repeated a private mutation")
	}
	names := make([]string, 0, controlpolicy.MaxApplicationKeys)
	for name := range maximumCapacityKeyKinds() {
		names = append(names, name)
	}
	sort.Strings(names)
	// Check presence and aggregate byte length inside the actual workload,
	// without printing values, their lengths, or hashes. Missing padding keys
	// must not turn a nominal maximum-budget test into a smaller delivery.
	command := `__loki_bytes=0; for __loki_name in ` + strings.Join(names, " ") + `; do eval '__loki_present=${'"$__loki_name"'+x}; __loki_length=${#'"$__loki_name"'}'; test "$__loki_present" = x || exit 17; __loki_bytes=$((__loki_bytes + __loki_length)); done; test "$__loki_bytes" -eq 0000000 || exit 18; printf '%s' "$TOKEN$S000$MAXA$MAXB$MAXC"; printf '%s' "$TOKEN" > /proc/1/fd/1; printf 'capacity-ready\n'; printf '%065536d\n' 0; unset PAD000; while :; do sleep 1; done`
	argv := []string{"/bin/sh", "-c", command}
	resources, err := b.currentApplicationCatalog()
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := b.migrationMetadata(ctx, state, resources, false)
	if err != nil {
		t.Fatal(err)
	}
	capacityValues, patterns := maximumCapacityValues(t, metadata.BaseEnvironment, argv)
	valueBytes := 0
	for _, value := range capacityValues {
		valueBytes += len(value.body)
	}
	if len(strconv.Itoa(valueBytes)) != 7 || strings.Count(argv[2], "0000000") != 1 {
		t.Fatal("invalid public aggregate capacity budget")
	}
	// Equal-width replacement preserves the exact exec budget already checked.
	argv[2] = strings.Replace(argv[2], "0000000", strconv.Itoa(valueBytes), 1)
	defer func() {
		for _, value := range capacityValues {
			clear(value.body)
		}
	}()
	matcher, err := secretoutput.New(patterns)
	if err != nil {
		t.Fatal("maximum-state accepted payload rejected", err)
	}
	matcher.Destroy()
	boundary := append(append([][]byte(nil), patterns...), []byte("z"))
	if rejected, err := secretoutput.New(boundary); err == nil {
		rejected.Destroy()
		t.Fatal("one-state-over payload accepted")
	}
	// Staged imports also preserve existing values. Explicitly remove the
	// three fixture layers before importing the new maximum-capacity set.
	for _, name := range []string{"TOKEN", "API_URL", "MAXA"} {
		revision := must("secret_inspect", map[string]any{"action": "values", "resource": "fixture", "limit": 1})["revision"]
		must("secret_delete", map[string]any{"action": "unset", "resource": "fixture", "name": name, "kind": maximumCapacityKeyKinds()[name], "expected_revision": revision, "request_id": privateio.OperationID(b.root, "capacity-unset", name)})
	}
	for offset, batch := 0, 0; offset < len(capacityValues); batch++ {
		var input bytes.Buffer
		var variables []string
		for offset < len(capacityValues) {
			value := capacityValues[offset]
			if input.Len()+len(value.name)+len(value.body)+2 > 490000 {
				break
			}
			input.WriteString(value.name + "=")
			input.Write(value.body)
			input.WriteByte('\n')
			if value.kind == "variable" {
				variables = append(variables, value.name)
			}
			offset++
		}
		args := []string{"app", "stage", "fixture", "--principal", "agent", "--stdin"}
		if len(variables) > 0 {
			args = append(args, "--variables", strings.Join(variables, ","))
		}
		staged := fixtureApplicationAdmin(t, ctx, b, state, input.Bytes(), args...)
		clear(input.Bytes())
		revision := must("secret_inspect", map[string]any{"action": "values", "resource": "fixture", "limit": 1})["revision"]
		must("secret_write", map[string]any{"action": "import_staged", "resource": "fixture", "stage_id": staged["stage_id"], "expected_revision": revision, "request_id": privateio.OperationID(b.root, "capacity-import", strconv.Itoa(batch))})
	}
	broker, err := b.applicationBackupContainer(ctx, state)
	if err != nil {
		t.Fatal("shipping broker limits/confinement", err)
	}
	base := map[string]any{"action": "start", "cwd": ".", "resource": "fixture", "env": "common", "network": "none", "timeout_seconds": 120, "argv": argv}
	args := func(i int) map[string]any {
		a := map[string]any{}
		for k, v := range base {
			a[k] = v
		}
		a["request_id"] = privateio.OperationID(b.root, "maximum-capacity", strconv.Itoa(i))
		return a
	}
	type started struct {
		index int
		id    string
		err   error
	}
	ids := make([]string, 8)
	for i := range ids {
		id, err := jobs.JobIDForRequestID(args(i)["request_id"].(string))
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if owned, err := mcpclient.New(cleanup, endpoint, token); err == nil {
			defer owned.Close()
			for _, id := range ids {
				_, _ = owned.Call(cleanup, "job", map[string]any{"action": "cancel", "job_id": id})
			}
		}
		if err := b.BeginJobMaintenance(cleanup, privateio.OperationID(b.root, "cleanup", "maximum-capacity"), true); err != nil {
			t.Error("owned protected Job cleanup could not be confirmed", err)
		}
	}()
	startedJobs := make(chan started, 8)
	for i := 0; i < 8; i++ {
		go func(i int) {
			result, err := call("job", args(i))
			id, _ := result["job_id"].(string)
			startedJobs <- started{i, id, err}
		}(i)
	}
	var admissionError error
	for range 8 {
		select {
		case result := <-startedJobs:
			if result.err != nil {
				admissionError = fmt.Errorf("parallel maximum-payload admission failed: %w", result.err)
			} else if result.id != ids[result.index] {
				admissionError = errors.New("parallel maximum-payload admission failed")
			}
		case <-ctx.Done():
			t.Fatal("parallel admission timed out")
		}
	}
	if admissionError != nil {
		t.Fatal(admissionError)
	}
	for _, id := range ids {
		for {
			status := must("job", map[string]any{"action": "inspect", "job_id": id})
			output := must("job", map[string]any{"action": "output", "job_id": id})
			body, _ := output["output"].(string)
			for _, pattern := range patterns {
				if bytes.Contains([]byte(body), pattern) {
					t.Fatal("private value crossed authenticated output")
				}
			}
			if status["state"] == "terminal" {
				t.Fatal("maximum-payload job failed before concurrent observation")
			}
			if status["state"] == "running" && strings.Contains(body, "capacity-ready") && strings.Contains(body, "[REDACTED]") {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("eight simultaneous maximum-payload jobs unavailable")
			case <-time.After(100 * time.Millisecond):
			}
		}
		metadata, err := b.runner.(PrivateRunner).RunPrivate(ctx, nil, nil, "container", "inspect", "loki-job-"+id)
		if err != nil {
			t.Fatal("exact external Job observation", err)
		}
		for _, pattern := range patterns {
			encoded, _ := json.Marshal(string(pattern))
			leaked := bytes.Contains(metadata, pattern) || bytes.Contains(metadata, encoded[1:len(encoded)-1])
			clear(encoded)
			if leaked {
				t.Fatal("private input reached Docker metadata")
			}
		}
		var containers []struct {
			LogPath    string
			HostConfig struct{ LogConfig struct{ Type string } }
		}
		if json.Unmarshal(metadata, &containers) != nil || len(containers) != 1 || containers[0].LogPath != "" || containers[0].HostConfig.LogConfig.Type != "none" {
			t.Fatal("raw workload logging enabled")
		}
	}
	if replay := must("job", args(0)); replay["job_id"] != ids[0] || replay["replayed"] != true {
		t.Fatal("one request created another protected Job")
	}
	if _, err := call("job", args(8)); err == nil {
		t.Fatal("ninth protected Job admitted")
	}
	metrics, err := b.runner.(PrivateRunner).RunPrivate(ctx, nil, nil, "exec", "--user", "10006:10006", broker, "/bin/sh", "-ec", `printf 'memory_peak '; cat /sys/fs/cgroup/memory.peak; printf 'pids_peak '; cat /sys/fs/cgroup/pids.peak; printf 'memory_events\n'; cat /sys/fs/cgroup/memory.events; printf 'pids_events\n'; cat /sys/fs/cgroup/pids.events`)
	if err != nil {
		t.Fatal("actual broker cgroup evidence", err)
	}
	observed := map[string]uint64{}
	scope := ""
	for _, line := range strings.Split(string(metrics), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 1 {
			scope = fields[0]
		} else if len(fields) == 2 {
			value, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				t.Fatal("invalid cgroup measurement")
			}
			observed[scope+"/"+fields[0]] = value
		}
	}
	if observed["/memory_peak"] == 0 || observed["/memory_peak"] > 1<<30 || observed["/pids_peak"] == 0 || observed["/pids_peak"] > 64 || observed["memory_events/oom"] != 0 || observed["memory_events/oom_kill"] != 0 || observed["pids_events/max"] != 0 {
		t.Fatal("shipping broker resource budget exceeded")
	}
	t.Log(fmt.Sprintf("eight concurrent maximum-state payloads: broker memory.peak=%d/1073741824 pids.peak=%d/64, no OOM/PID-limit events", observed["/memory_peak"], observed["/pids_peak"]))
	for _, id := range ids {
		must("job", map[string]any{"action": "cancel", "job_id": id})
		status := must("job", map[string]any{"action": "inspect", "job_id": id})
		if status["state"] != "terminal" || status["cleanup"] != "complete" {
			t.Fatal("exact protected Job cleanup incomplete")
		}
	}
}
