package mcpapp

import (
	"sort"
	"strings"

	"loki/internal/process"
)

func toolEnvironment(overrides map[string]string) []string {
	values := map[string]string{}
	for _, entry := range process.Environment() {
		name, value, _ := strings.Cut(entry, "=")
		values[name] = value
	}
	for name, value := range map[string]string{
		"HTTPS_PROXY": "http://127.0.0.1:18766", "HTTP_PROXY": "http://127.0.0.1:18766",
		"https_proxy": "http://127.0.0.1:18766", "http_proxy": "http://127.0.0.1:18766",
		"NO_PROXY": "127.0.0.1,localhost", "no_proxy": "127.0.0.1,localhost",
		"GIT_CONFIG_GLOBAL": "/etc/loki-go/gitconfig", "SSH_AUTH_SOCK": "/run/loki-go/signing/agent.sock",
		"PATH": "/opt/loki/toolchain/bin:/opt/loki/bin:/usr/local/bin:/usr/bin:/bin",
	} {
		values[name] = value
	}
	for name, value := range overrides {
		values[name] = value
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, name+"="+values[name])
	}
	return result
}
