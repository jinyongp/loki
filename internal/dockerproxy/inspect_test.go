package dockerproxy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"loki/internal/portguard"
)

func TestComposePortInspection(t *testing.T) {
	for _, mode := range []string{"trusted", "public-binding", "outside", "config-traversal", "multiple", "invalid-id", "invalid-json", "stopped"} {
		t.Run(mode, func(t *testing.T) {
			labels := map[string]string{compose + "project": "example", compose + "service": "web", compose + "project.working_dir": "/srv/workspace/repo", compose + "project.config_files": "/srv/workspace/repo/compose.yaml"}
			if mode == "outside" {
				labels[compose+"project.working_dir"] = "/etc"
			}
			if mode == "config-traversal" {
				labels[compose+"project.config_files"] = "/srv/workspace/repo/../compose.yaml"
			}
			encoded, _ := json.Marshal(labels)
			i := Inspector{Workspace: "/srv/workspace", SnapshotRoot: "/var/lib/loki/snapshots", run: func(ctx context.Context, args ...string) (string, error) {
				if args[0] == "container" {
					if mode == "invalid-id" {
						return "--malicious", nil
					}
					if mode == "multiple" {
						return "0123456789ab\nabcdef012345", nil
					}
					return "0123456789ab", nil
				}
				ip := "127.0.0.1"
				if mode == "public-binding" {
					ip = "0.0.0.0"
				}
				running := "true"
				if mode == "stopped" {
					running = "false"
				}
				if mode == "invalid-json" {
					return "true\tbad\t{}", nil
				}
				return running + "\t{\"80/tcp\":[{\"HostIp\":\"" + ip + "\",\"HostPort\":\"43000\"}]}\t" + string(encoded), nil
			}}
			result, err := i.Inspect(t.Context(), 43000)
			if mode == "multiple" || strings.HasPrefix(mode, "invalid-") {
				if err == nil {
					t.Fatal(result)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result["in_use"] != (mode == "trusted") {
				t.Fatal(result)
			}
			if mode == "trusted" {
				row := result["listeners"].([]map[string]any)[0]
				if row["cwd"] != "/workspace/repo" || row["command"] != "docker-compose:example/web" {
					t.Fatal(row)
				}
			}
		})
	}
}

func TestInspectorRejectsProtectedPortBeforeDocker(t *testing.T) {
	ports, err := portguard.NewPolicy(18765)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	inspector := Inspector{Ports: ports, run: func(context.Context, ...string) (string, error) {
		calls++
		return "", nil
	}}
	if _, err := inspector.Inspect(t.Context(), 18765); !errors.Is(err, portguard.ErrProtected) {
		t.Fatalf("protected port error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("protected port invoked Docker %d times", calls)
	}
}
