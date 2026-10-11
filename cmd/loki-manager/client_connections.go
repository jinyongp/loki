package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"

	hostconfig "loki/internal/config"
	"loki/internal/management"
)

type clientConnection struct {
	Provider   string   `json:"provider"`
	Configured bool     `json:"configured"`
	Config     string   `json:"config,omitempty"`
	Servers    []string `json:"servers,omitempty"`
	Enabled    *bool    `json:"enabled,omitempty"`
	Issue      string   `json:"issue,omitempty"`
}

func defaultCodexConfig() (string, error) {
	if directory := os.Getenv("CODEX_HOME"); directory != "" {
		return filepath.Join(directory, "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	return filepath.Join(home, ".codex", "config.toml"), err
}

func readCodexConfiguration(config string) ([]byte, error) {
	info, err := os.Lstat(config)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return nil, fmt.Errorf("Codex configuration must be a bounded regular file")
	}
	return os.ReadFile(config)
}

func inspectCodexConnection() clientConnection {
	view := clientConnection{Provider: "codex", Servers: []string{}}
	config, err := defaultCodexConfig()
	view.Config = config
	if err != nil {
		view.Issue = "Cannot locate Codex configuration"
		return view
	}
	raw, err := readCodexConfiguration(config)
	if err != nil {
		view.Issue = "Cannot read Codex configuration; inspect its path and permissions"
		return view
	}
	var document struct {
		Servers map[string]any `toml:"mcp_servers"`
	}
	if toml.Unmarshal(raw, &document) != nil {
		view.Issue = "Codex configuration is invalid; repair it before connecting"
		return view
	}
	for _, name := range []string{"loki", "loki_browser"} {
		if document.Servers[name] != nil {
			view.Servers = append(view.Servers, name)
		}
	}
	view.Configured = len(view.Servers) > 0
	return view
}

func showClientConnections(store management.Store, out io.Writer) error {
	clients := []clientConnection{inspectCodexConnection()}
	managed, err := inspectManagedConnections(store)
	if err != nil {
		return err
	}
	clients = append(clients, managed...)
	if jsonOutput(out) {
		return result(out, "Loki client connections", map[string]any{"connections": clients})
	}
	fmt.Fprintln(out, "Loki client connections (on this computer)")
	for _, client := range clients {
		state := "not configured"
		if client.Configured {
			state = "configured; runtime not checked"
		}
		if client.Issue != "" {
			state = client.Issue
		}
		fmt.Fprintf(out, "  %s: %s\n", client.Provider, state)
		if client.Config != "" {
			fmt.Fprintln(out, "    Configuration:", client.Config)
		}
	}
	fmt.Fprintln(out, "\nRun 'loki setup' to configure or manage connections.")
	return nil
}

func disconnectCodex(config string, out io.Writer) error {
	old, err := readCodexConfiguration(config)
	if err != nil {
		return err
	}
	if len(old) == 0 {
		return success(out, "Codex has no Loki connection to remove.", map[string]any{"changed": false})
	}
	lock, err := os.OpenFile(config+".loki-lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("Codex configuration is locked; retry after its current update: %w", err)
	}
	lock.Close()
	defer os.Remove(config + ".loki-lock")
	next := old
	for _, entry := range []struct{ server, begin, end string }{{"loki_browser", codexBegin, codexEnd}, {"loki", "# BEGIN Loki tools\n", "# END Loki tools\n"}} {
		if !bytes.Contains(next, []byte(entry.begin)) && !bytes.Contains(next, []byte(entry.end)) {
			continue
		}
		next, err = hostconfig.MergeClientConnection(next, nil, entry.server, entry.begin, entry.end)
		if err != nil {
			return err
		}
	}
	if bytes.Equal(old, next) {
		return success(out, "No Loki-managed Codex entries found. User-managed entries are retained.", map[string]any{"changed": false})
	}
	if err := writeCodexConfiguration(config, old, next); err != nil {
		return err
	}
	return success(out, "Loki disconnected from Codex. Other settings, tool programs and data are retained. Reopen the Codex project.", map[string]any{"changed": true, "config": config})
}
