package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	hostconfig "loki/internal/config"
	"loki/internal/management"
)

type clientBinding struct {
	Server string `json:"server"`
	HostID string `json:"host_id,omitempty"`
	Host   string `json:"host,omitempty"`
}
type ownedCodexEntry struct{ Name, HostID, Begin, End string }

var connectionNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,95}$`)
var namedCodexBegin = regexp.MustCompile(`(?m)^# BEGIN Loki connection ([a-z][a-z0-9_-]{0,95})\n`)

func ownedCodexEntries(raw []byte) []ownedCodexEntry {
	entries := []ownedCodexEntry{}
	for _, pair := range []ownedCodexEntry{{Name: "loki", Begin: "# BEGIN Loki tools\n", End: "# END Loki tools\n"}, {Name: "loki_browser", Begin: codexBegin, End: codexEnd}} {
		if bytes.Contains(raw, []byte(pair.Begin)) {
			entries = append(entries, pair)
		}
	}
	for _, match := range namedCodexBegin.FindAllStringSubmatch(string(raw), -1) {
		name := match[1]
		entries = append(entries, ownedCodexEntry{Name: name, Begin: match[0], End: "# END Loki connection " + name + "\n"})
	}
	for i, entry := range entries {
		a := bytes.Index(raw, []byte(entry.Begin))
		b := bytes.Index(raw[a:], []byte(entry.End))
		if b < 0 {
			continue
		}
		block := string(raw[a : a+b])
		if match := regexp.MustCompile(`(?m)^# host-id=([a-f0-9]{32})$`).FindStringSubmatch(block); match != nil {
			entries[i].HostID = match[1]
		}
	}
	return entries
}
func codexEntryMarkers(name string) (string, string) {
	return "# BEGIN Loki connection " + name + "\n", "# END Loki connection " + name + "\n"
}
func disconnectCodexEntries(config string, expected []byte, entries []ownedCodexEntry, out io.Writer) error {
	if len(entries) == 0 {
		return success(out, "No matching Loki-managed Codex connections found.", map[string]any{"changed": false})
	}
	unlock, err := lockCodexConfiguration(config)
	if err != nil {
		return err
	}
	defer unlock()
	old, err := readCodexConfiguration(config)
	if err != nil {
		return err
	}
	if !bytes.Equal(old, expected) {
		return fmt.Errorf("Codex configuration changed during selection; retry disconnect")
	}
	next := old
	for _, entry := range entries {
		next, err = hostconfig.MergeClientConnection(next, nil, entry.Name, entry.Begin, entry.End)
		if err != nil {
			return err
		}
	}
	if bytes.Equal(next, old) {
		return success(out, "Codex connections are unchanged.", map[string]any{"changed": false})
	}
	if err := writeCodexConfiguration(config, old, next); err != nil {
		return err
	}
	return success(out, "Selected Codex connections removed. Other hosts, tools and data are retained.", map[string]any{"changed": true})
}
func disconnectCodexTarget(store management.Store, selected *management.ExecutionSelection, name string, all bool, out io.Writer) error {
	config, err := defaultCodexConfig()
	if err != nil {
		return err
	}
	raw, err := readCodexConfiguration(config)
	if err != nil {
		return err
	}
	entries := []ownedCodexEntry{}
	target := management.ExecutionSelection{Schema: 1, Command: "loki"}
	r, err := store.HostRegistry()
	if err != nil {
		return err
	}
	h := r.Selected()
	if selected != nil {
		target = *selected
		h, err = store.HostForTarget(target)
		if err != nil && !all && name == "" {
			return err
		}
	}
	for _, entry := range ownedCodexEntries(raw) {
		if all || name != "" && entry.Name == name || name == "" && (entry.HostID == h.ID || entry.HostID == "" && legacyCodexEntryMatches(raw, entry, &h.Target, store.Root)) {
			entries = append(entries, entry)
		}
	}
	return disconnectCodexEntries(config, raw, entries, out)
}
func codexConnectionCommand(store management.Store, selected *management.ExecutionSelection, args []string, out io.Writer) (bool, error) {
	if len(args) < 2 || args[1] != "codex" || args[0] == "setup" {
		return false, nil
	}
	if args[0] == "status" || args[0] == "show" {
		return true, showClientConnections(store, out)
	}
	if args[0] != "remove" {
		return false, nil
	}
	name := ""
	all := false
	for i := 2; i < len(args); i++ {
		if args[i] == "--name" && i+1 < len(args) {
			i++
			name = args[i]
		} else if args[i] == "--all" {
			all = true
		} else {
			return true, fmt.Errorf("usage: loki connections remove codex [--name NAME | --all]")
		}
	}
	if name != "" && all {
		return true, fmt.Errorf("choose --name or --all")
	}
	return true, disconnectCodexTarget(store, selected, name, all, out)
}
func codexBindings(raw []byte, store management.Store) []clientBinding {
	bindings := []clientBinding{}
	for _, entry := range ownedCodexEntries(raw) {
		binding := clientBinding{Server: entry.Name, HostID: entry.HostID}
		if entry.HostID != "" {
			if h, err := store.RegisteredHost(entry.HostID); err == nil {
				binding.Host = h.Name
			} else {
				binding.Host = "unregistered host"
			}
		} else {
			binding.Host = "legacy fixed target; inspect connection arguments"
		}
		bindings = append(bindings, binding)
	}
	return bindings
}
func legacyCodexMatches(raw []byte, target *management.ExecutionSelection, nativeRoot ...string) bool {
	for _, entry := range ownedCodexEntries(raw) {
		if entry.HostID == "" && legacyCodexEntryMatches(raw, entry, target, nativeRoot...) {
			return true
		}
	}
	return false
}
func legacyCodexEntryMatches(raw []byte, entry ownedCodexEntry, target *management.ExecutionSelection, nativeRoot ...string) bool {
	// Legacy entries remain fixed argument snapshots; only adopt a matching owned
	// block. Do not infer ownership merely from a user-chosen server name.
	if !bytes.Contains(raw, []byte(entry.Begin)) {
		return false
	}
	args, err := hostconfig.ClientServerArguments(raw, entry.Name)
	if err != nil {
		return false
	}
	values := map[string]string{}
	for i := 0; i+1 < len(args); i++ {
		if strings.HasPrefix(args[i], "--") {
			values[args[i]] = args[i+1]
			i++
		}
	}
	if target == nil || target.Host.Kind == "local" && !target.System {
		sameRoot := len(nativeRoot) == 0 || values["--root"] == "" || filepath.Clean(values["--root"]) == filepath.Clean(nativeRoot[0])
		return sameRoot && (values["--host"] == "local" || values["--host"] == "") && values["--system-socket"] == ""
	}
	return values["--host"] == target.Host.Kind && values["--distribution"] == target.Host.Distribution && values["--address"] == target.Host.Address && values["--root"] == target.Root && values["--remote-user"] == target.User && values["--system-socket"] == target.Socket
}

func lockCodexConfiguration(path string) (func(), error) {
	f, err := os.OpenFile(path+".loki-lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("Codex configuration is locked: %w", err)
	}
	f.Close()
	return func() { os.Remove(path + ".loki-lock") }, nil
}
