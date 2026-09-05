package action

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestNodeResolutionAndSafeMetadata(t *testing.T) {
	root := t.TempDir()
	workspace, err := pinned(root, true)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	write := func(name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "repo"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := nodeVersion(workspace, "repo"); err == nil {
		t.Fatal("invented Node version")
	}
	write("repo/.nvmrc", "  22\n")
	write("repo/.node-version", "v24.20.0\n")
	for name, want := range map[string][]string{
		"node":       {"/home/linuxbrew/.linuxbrew/bin/fnm", "exec", "--using", "v24.20.0", "node", "--version"},
		"npm":        {"/home/linuxbrew/.linuxbrew/bin/fnm", "exec", "--using", "v24.20.0", "npm", "--version"},
		"pnpm":       {"/home/linuxbrew/.linuxbrew/bin/fnm", "exec", "--using", "v24.20.0", "corepack", "pnpm", "--version"},
		"just":       {"/home/linuxbrew/.linuxbrew/bin/fnm", "exec", "--using", "v24.20.0", "/home/linuxbrew/.linuxbrew/bin/just", "--version"},
		"actions-up": {"/home/linuxbrew/.linuxbrew/bin/fnm", "exec", "--using", "v24.20.0", "/home/linuxbrew/.linuxbrew/bin/actions-up", "--version"},
	} {
		got, err := resolvedCommand([]string{name, "--version"}, workspace, "repo")
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s resolution: %v %v", name, got, err)
		}
	}
	write("repo/.node-version", "bad/version")
	if got, err := nodeVersion(workspace, "repo"); err != nil || got != "22" {
		t.Fatalf("invalid hint fallback: %s %v", got, err)
	}
	if err := os.Remove(filepath.Join(root, "repo/.node-version")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "repo/.node-version")); err != nil {
		t.Fatal(err)
	}
	if got, err := nodeVersion(workspace, "repo"); err != nil || got != "22" {
		t.Fatal("symlink hint followed")
	}
	if err := os.Remove(filepath.Join(root, "repo/.node-version")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "repo/.node-version"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := nodeVersion(workspace, "repo"); err != nil || got != "22" {
		t.Fatal("non-regular version metadata")
	}
	if err := os.Remove(filepath.Join(root, "repo/.node-version")); err != nil {
		t.Fatal(err)
	}
	write("repo/.node-version", strings.Repeat(" ", 4097))
	if _, err := nodeVersion(workspace, "repo"); err == nil {
		t.Fatal("unbounded version hint")
	}
	write("repo/.node-version", string([]byte{0xff}))
	if _, err := nodeVersion(workspace, "repo"); err == nil {
		t.Fatal("non-UTF8 hint accepted")
	}
	write("repo/.node-version", "")
	write("repo/.nvmrc", "")
	for _, name := range []string{"v9.20.0", "v24.9.0", "v24.10.0", "v24.10.1", "v24.10.1-beta"} {
		if err := os.MkdirAll(filepath.Join(root, ".loki/fnm/node-versions", name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := nodeVersion(workspace, "repo"); err != nil || got != "v24.10.1" {
		t.Fatalf("numeric maximum: %s %v", got, err)
	}
	if _, err := resolvedCommand([]string{"node", "-e", "process.exit()"}, workspace, "repo"); err == nil {
		t.Fatal("Node command policy bypassed")
	}
}

func TestNodeActionSandbox(t *testing.T) {
	// Mount only the source distro's existing public toolchain read-only. All
	// project/state/output files belong to this disposable test fixture.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	fnmRoot := filepath.Join(home, ".local/share/fnm")
	entries, err := os.ReadDir(filepath.Join(fnmRoot, "node-versions"))
	version := ""
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() && installedVersion.MatchString(entry.Name()) {
				version = entry.Name()
				break
			}
		}
	}
	probe := exec.CommandContext(t.Context(), "/usr/bin/bwrap", "--unshare-all", "--ro-bind", "/usr", "/usr", "--symlink", "usr/lib", "/lib", "--symlink", "usr/lib64", "/lib64", "--", "/usr/bin/true")
	if err := probe.Run(); err != nil || os.Getuid() == 0 || version == "" {
		if os.Getenv("LOKI_REQUIRE_SANDBOX_TESTS") == "1" {
			t.Fatalf("Node sandbox requires non-root bubblewrap and an installed public FNM Node version: %v", err)
		}
		t.Skip("non-root bubblewrap/FNM Node fixture unavailable")
	}
	busy, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	preferred := busy.Addr().(*net.TCPAddr).Port
	r, _ := runtimeFixture(t, map[string]any{"command": []string{"node", "server.cjs", "{LOKI_PORT}", "literal-{LOKI_PORT}"}, "singleton": true,
		"dynamic_port": map[string]any{"preferred": preferred, "environment": "PORT", "origin_environment": "ORIGIN"}})
	r.layout.Binary = candidateBinary(t)
	r.layout.PublicMounts = []PublicMount{{"/home/linuxbrew/.linuxbrew", "/home/linuxbrew/.linuxbrew"}, {fnmRoot, "/home/runner/.local/share/fnm"}}
	program := `const http = require('node:http');
const actual = {version:process.version, cwd:process.cwd(), port:process.env.PORT, origin:process.env.ORIGIN, api:process.env.PUBLIC_API, argv:process.argv.slice(2)};
console.log(process.env.TOKEN);
http.createServer((req,res) => {res.setHeader('Content-Type','application/json'); res.end(JSON.stringify(actual))}).listen(Number(process.env.PORT),'127.0.0.1',() => console.log('ready'));
`
	for name, data := range map[string]string{".node-version": version, "server.cjs": program} {
		if err := os.WriteFile(filepath.Join(r.layout.Workspace, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	prepared, err := r.Prepare(t.Context(), PrepareRequest{Profile: "fixture", Action: "web"})
	if err != nil {
		t.Fatal(err)
	}
	token := prepared["launch_token"].(string)
	base := "https://loki-" + strings.Repeat("a", 32) + ".preview.example.test"
	request := RunRequest{Profile: "fixture", Action: "web", LaunchToken: &token, PublicEnvironment: map[string]string{"ORIGIN": base, "PUBLIC_API": base + "/api/v1"}}
	started, err := r.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	portValue, err := started["port"].(json.Number).Int64()
	if err != nil {
		t.Fatal(err)
	}
	port := int(portValue)
	if port == preferred || port != prepared["port"] || started["local_url"] != localURL(port) {
		t.Fatalf("port reservation changed: %#v", started)
	}
	reused, err := r.Run(t.Context(), request)
	if err != nil || reused["session_id"] != started["session_id"] || reused["reused"] != true || reused["port"] != started["port"] {
		t.Fatalf("singleton consumed token twice: %#v %v", reused, err)
	}
	id := started["session_id"].(string)
	deadline := time.Now().Add(10 * time.Second)
	for {
		value, err := r.Read(id, nil, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(value["output"].(string), "ready\n") {
			break
		}
		if value["status"] == "exited" || time.Now().After(deadline) {
			t.Fatalf("Node action failed to listen: %#v", value)
		}
		time.Sleep(10 * time.Millisecond)
	}
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	response, err := client.Get(localURL(port))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]any
	if err := json.Unmarshal(data, &actual); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"version": version, "cwd": "/workspace", "port": strconv.Itoa(port), "origin": base, "api": base + "/api/v1", "argv": []any{strconv.Itoa(port), "literal-{LOKI_PORT}"}}
	if response.StatusCode != 200 || !reflect.DeepEqual(actual, want) {
		t.Fatalf("sandbox Node environment/arguments: %#v", actual)
	}
	stopped, err := r.Stop(id)
	if err != nil || stopped["status"] != "exited" {
		t.Fatalf("Node stop: %#v %v", stopped, err)
	}
	stopped, err = r.Read(id, nil, 4096)
	if err != nil || !strings.Contains(stopped["output"].(string), "[REDACTED]") || strings.Contains(stopped["output"].(string), "synthetic-action-value") {
		t.Fatalf("Node shutdown/output: %#v %v", stopped, err)
	}
	if connection, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second); err == nil {
		connection.Close()
		t.Fatal("stopped action still accepts connections")
	}
	if _, err := r.Run(t.Context(), request); err == nil || err.Error() != "action launch token is unknown or expired" {
		t.Fatalf("completed singleton token replay: %v", err)
	}
	t.Log("Actual Go sandbox -> FNM -> Node server -> loopback HTTP -> explicit stop passed; public URLs were injected test values, not external routing checks")
	encoded, _ := json.Marshal(map[string]any{"command": []string{"node", "server.cjs"}, "cwd": ".", "secrets": []string{"TOKEN"}, "all_secrets": false, "timeout_seconds": 30, "max_output_bytes": 4096, "singleton": true, "local_callback": true, "dynamic_port": map[string]any{"preferred": preferred, "environment": "PORT"}})
	if _, err := r.controller.SetAction(t.Context(), "fixture", "api", encoded); err != nil {
		t.Fatal(err)
	}
	callbackRequest := RunRequest{Profile: "fixture", Action: "api", BindLocalCallback: true}
	api, err := r.Run(t.Context(), callbackRequest)
	if err != nil {
		t.Fatal(err)
	}
	binding := api["local_callback"].(map[string]any)
	if binding["bound"] != true {
		t.Fatalf("callback not bound: %#v", binding)
	}
	apiID := api["session_id"].(string)
	deadline = time.Now().Add(10 * time.Second)
	for {
		snapshot, err := r.Read(apiID, nil, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(snapshot["output"].(string), "ready\n") {
			break
		}
		if snapshot["status"] == "exited" || time.Now().After(deadline) {
			t.Fatalf("API not ready: %#v", snapshot)
		}
		time.Sleep(10 * time.Millisecond)
	}
	response, err = client.Get(binding["origin"].(string) + "/callback?code=opaque")
	if err != nil {
		t.Fatal(err)
	}
	data, err = io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var callbackData map[string]any
	if err := json.Unmarshal(data, &callbackData); err != nil {
		t.Fatal(err)
	}
	if callbackData["port"] != api["port"].(json.Number).String() {
		t.Fatalf("callback reached wrong API: %#v", callbackData)
	}
	again, err := r.Run(t.Context(), callbackRequest)
	if err != nil || again["session_id"] != api["session_id"] || again["local_callback"].(map[string]any)["bound"] != true {
		t.Fatalf("callback singleton: %#v %v", again, err)
	}
	if _, err := r.Stop(apiID); err != nil {
		t.Fatal(err)
	}
	if r.CallbackStatus()["bound"] != false || r.CallbackStatus()["session_id"] != nil {
		t.Fatal(r.CallbackStatus())
	}
	response, err = client.Get(binding["origin"].(string))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 503 {
		t.Fatalf("stopped callback status: %d", response.StatusCode)
	}
	for _, mode := range []string{"plain", "fixed", "session"} {
		t.Run("docker-"+mode, func(t *testing.T) {
			docker, controller, _ := materializationFixture(t, r.layout.Binary, mode == "fixed", "BEGIN { exit }")
			docker.layout.PublicMounts = r.layout.PublicMounts
			docker.layout.DockerDirectory = t.TempDir()
			docker.layout.DockerUID = uint32(os.Getuid())
			docker.layout.DockerSocket = filepath.Join(t.TempDir(), "daemon.sock")
			listener, err := net.Listen("unix", docker.layout.DockerSocket)
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/_ping" {
					t.Errorf("unexpected Docker request: %s", request.URL.Path)
				}
				io.WriteString(w, "OK")
			})}
			go server.Serve(listener)
			defer server.Close()
			program := `const fs=require('node:fs'),http=require('node:http');
const actual={cwd:process.cwd(),mode:process.env.DOCKER_ACCESS_MODE,materialized:false,readonly:false};
if(process.env.ENV_FILE){const data=fs.readFileSync(process.env.ENV_FILE,'utf8'); const visible='/workspace'+process.env.ENV_FILE.slice(process.cwd().length); if(data!==fs.readFileSync(visible,'utf8')||!data.includes('TOKEN='))process.exit(4); actual.materialized=true;try{fs.openSync(process.env.ENV_FILE,'w');process.exit(5)}catch{actual.readonly=true}}
http.get({socketPath:process.env.DOCKER_HOST.slice('unix://'.length),path:'/_ping'},res=>{let data='';res.on('data',x=>data+=x);res.on('end',()=>{actual.reply=data;console.log('docker-result:'+JSON.stringify(actual))})}).on('error',()=>process.exit(6));`
			for name, data := range map[string]string{".node-version": version, "docker.cjs": program} {
				if err := os.WriteFile(filepath.Join(docker.layout.Workspace, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			policy := map[string]any{"command": []string{"node", "docker.cjs"}, "cwd": ".", "secrets": []string{"TOKEN"}, "all_secrets": false, "timeout_seconds": 10, "max_output_bytes": 4096, "docker_access": true}
			if mode != "plain" {
				policy["materialize_env_file"] = "ENV_FILE"
				if mode == "fixed" {
					policy["materialize_env_path"] = "fixture.env"
				}
			}
			encoded, _ := json.Marshal(policy)
			if _, err := controller.SetAction(t.Context(), "fixture", "check", encoded); err != nil {
				t.Fatal(err)
			}
			started, err := docker.Run(t.Context(), RunRequest{Profile: "fixture", Action: "check"})
			if err != nil {
				t.Fatal(err)
			}
			id := started["session_id"].(string)
			deadline := time.Now().Add(10 * time.Second)
			var completed map[string]any
			for {
				completed, err = docker.Read(id, nil, 4096)
				if err != nil {
					t.Fatal(err)
				}
				if completed["status"] == "exited" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("Docker action timeout")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if completed["exit_code"] != 0 || completed["cleanup_error"] != nil {
				t.Fatalf("Docker sandbox: %#v", completed)
			}
			var actual map[string]any
			_, record, found := strings.Cut(completed["output"].(string), "docker-result:")
			if !found {
				t.Fatalf("Docker result missing: %#v", completed)
			}
			if err := json.Unmarshal([]byte(record), &actual); err != nil {
				t.Fatalf("%v %#v", err, completed)
			}
			if actual["cwd"] != docker.layout.Workspace || actual["mode"] != "restricted-proxy" || actual["reply"] != "OK" || actual["materialized"] != (mode != "plain") || actual["readonly"] != (mode != "plain") {
				t.Fatalf("Docker action: %#v", actual)
			}
			entries, err := os.ReadDir(docker.layout.DockerDirectory)
			if err != nil || len(entries) != 0 {
				t.Fatalf("Docker proxy survived completion: %v %v", entries, err)
			}
		})
	}
}
