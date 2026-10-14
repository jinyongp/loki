package management

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"loki/internal/tools"
)

// HostRecord is a frontend reference. Tool state and credentials stay at Target.Root.
// IDs survive renames and preparation; the default affects CLI selection only.
type HostRecord struct {
	ID               string             `json:"id"`
	Name             string             `json:"name"`
	Target           ExecutionSelection `json:"target"`
	LegacyNamespace  string             `json:"legacy_namespace,omitempty"`
	LegacyConnection bool               `json:"legacy_connection,omitempty"`
}
type HostRegistry struct {
	Schema  int          `json:"schema"`
	Default string       `json:"default"`
	Hosts   []HostRecord `json:"hosts"`
}

var hostIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var legacyNamespacePattern = regexp.MustCompile(`^ssh-[a-f0-9]{16}$`)
var hostNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

func ValidateHostName(name string) error {
	if !hostNamePattern.MatchString(name) || hostIDPattern.MatchString(name) || name == "wsl" || name == "ssh" {
		return fmt.Errorf("host name must use lowercase letters, digits, '-' or '_' (wsl/ssh and 32-digit hex IDs are reserved)")
	}
	return nil
}
func (r HostRegistry) Validate() error {
	if r.Schema != 1 || len(r.Hosts) == 0 || len(r.Hosts) > 128 {
		return fmt.Errorf("invalid host registry")
	}
	ids, names := map[string]bool{}, map[string]bool{}
	for _, h := range r.Hosts {
		if !hostIDPattern.MatchString(h.ID) || ids[h.ID] || names[h.Name] {
			return fmt.Errorf("duplicate or invalid registered host")
		}
		if err := ValidateHostName(h.Name); err != nil {
			return err
		}
		if h.Name == "local" && h.Target.Remote() {
			return fmt.Errorf("local is reserved for the native frontend host")
		}
		if h.LegacyNamespace != "" && (!h.LegacyConnection || !legacyNamespacePattern.MatchString(h.LegacyNamespace)) {
			return fmt.Errorf("invalid legacy connection namespace")
		}
		if err := h.Target.Validate(); err != nil {
			return err
		}
		for _, other := range r.Hosts {
			if other.ID != h.ID && sameHostInstallation(other.Target, h.Target) {
				return fmt.Errorf("registered hosts refer to the same installation")
			}
		}
		ids[h.ID], names[h.Name] = true, true
	}
	if !ids[r.Default] {
		return fmt.Errorf("default host is not registered")
	}
	return nil
}
func (r HostRegistry) Find(nameOrID string) (HostRecord, error) {
	for _, h := range r.Hosts {
		if h.ID == nameOrID {
			return h, nil
		}
	}
	for _, h := range r.Hosts {
		if h.Name == nameOrID {
			return h, nil
		}
	}
	return HostRecord{}, fmt.Errorf("host %q is not registered; run 'loki hosts list'", nameOrID)
}
func (r HostRegistry) Selected() HostRecord {
	for _, h := range r.Hosts {
		if h.ID == r.Default {
			return h
		}
	}
	return HostRecord{}
}
func hostRecordID(root string, target ExecutionSelection) string {
	raw, _ := json.Marshal(struct {
		Root   string
		Target ExecutionSelection
	}{filepath.Clean(root), target})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}
func suggestedHostName(target ExecutionSelection) string {
	name := "local"
	if target.System {
		name = "system"
	} else if target.Host.Kind == "wsl" {
		name = strings.ToLower(target.Host.Distribution)
	} else if target.Host.Kind == "ssh" {
		name = "ssh-" + strings.ToLower(target.Host.Address)
	}
	name = regexp.MustCompile(`[^a-z0-9_-]+`).ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	if name == "" || name[0] < 'a' || name[0] > 'z' || name == "ssh" || name == "wsl" || hostIDPattern.MatchString(name) || target.Remote() && name == "local" {
		name = "host-" + name
	}
	if len(name) > 54 {
		name = name[:54]
	}
	return name
}
func sameHostInstallation(a, b ExecutionSelection) bool {
	return sameExecutionEnvironment(a.Host, b.Host) && a.User == b.User && a.System == b.System && a.Root == b.Root && a.Socket == b.Socket
}
func (s Store) HostRegistry() (HostRegistry, error) {
	if err := s.realRoot(); err != nil {
		return HostRegistry{}, err
	}
	if err := s.realControl(); err != nil {
		return HostRegistry{}, err
	}
	var r HostRegistry
	err := readOwnedJSON(filepath.Join(s.ControlDirectory(), "hosts.json"), tools.MaxManifestBytes, &r)
	if err == nil {
		return r, r.Validate()
	}
	if !errors.Is(err, os.ErrNotExist) {
		return r, err
	}
	local := ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "local"}, Command: "loki"}
	h := HostRecord{ID: hostRecordID(s.Root, local), Name: "local", Target: local}
	r = HostRegistry{Schema: 1, Default: h.ID, Hosts: []HostRecord{h}}
	var previous ExecutionSelection
	err = readOwnedJSON(filepath.Join(s.ControlDirectory(), "execution-host.json"), tools.MaxManifestBytes, &previous)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err = previous.Validate(); err != nil {
		return r, err
	}
	if !sameHostInstallation(local, previous) {
		h = HostRecord{ID: hostRecordID(s.Root, previous), Name: suggestedHostName(previous), Target: previous, LegacyConnection: true}
		h.freezeConnectionNamespace()
		r.Hosts = append(r.Hosts, h)
	}
	r.Default = h.ID
	return r, r.Validate()
}
func (s Store) saveHostRegistry(r HostRegistry) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if err := s.realControl(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.ControlDirectory(), 0700); err != nil {
		return err
	}
	sort.Slice(r.Hosts, func(i, j int) bool { return r.Hosts[i].Name < r.Hosts[j].Name })
	return atomicJSON(filepath.Join(s.ControlDirectory(), "hosts.json"), r)
}
func (s Store) RegisteredHost(nameOrID string) (HostRecord, error) {
	r, err := s.HostRegistry()
	if err != nil {
		return HostRecord{}, err
	}
	return r.Find(nameOrID)
}
func (s Store) HostForTarget(target ExecutionSelection) (HostRecord, error) {
	r, err := s.HostRegistry()
	if err != nil {
		return HostRecord{}, err
	}
	for _, h := range r.Hosts {
		if sameHostInstallation(h.Target, target) {
			return h, nil
		}
	}
	return HostRecord{}, fmt.Errorf("execution target is not registered")
}
func (s Store) RegisterHost(name string, target ExecutionSelection) (HostRecord, error) {
	if err := ValidateHostName(name); err != nil {
		return HostRecord{}, err
	}
	if err := target.Validate(); err != nil {
		return HostRecord{}, err
	}
	unlock, err := s.Lock()
	if err != nil {
		return HostRecord{}, err
	}
	defer unlock()
	r, err := s.HostRegistry()
	if err != nil {
		return HostRecord{}, err
	}
	for _, h := range r.Hosts {
		if h.Name == name || sameHostInstallation(h.Target, target) {
			return HostRecord{}, fmt.Errorf("host is already registered as %q", h.Name)
		}
	}
	id, err := newHostRecordID()
	if err != nil {
		return HostRecord{}, err
	}
	h := HostRecord{ID: id, Name: name, Target: target}
	r.Hosts = append(r.Hosts, h)
	return h, s.saveHostRegistry(r)
}
func (s Store) UseHost(name string) error {
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	r, err := s.HostRegistry()
	if err != nil {
		return err
	}
	h, err := r.Find(name)
	if err != nil {
		return err
	}
	r.Default = h.ID
	return s.saveHostRegistry(r)
}
func (s Store) RenameHost(name, next string) error {
	if err := ValidateHostName(next); err != nil {
		return err
	}
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	r, err := s.HostRegistry()
	if err != nil {
		return err
	}
	h, err := r.Find(name)
	if err != nil {
		return err
	}
	for i, entry := range r.Hosts {
		if entry.Name == next && entry.ID != h.ID {
			return fmt.Errorf("host name is already used")
		}
		if entry.ID == h.ID {
			r.Hosts[i].Name = next
		}
	}
	return s.saveHostRegistry(r)
}
func (s Store) selectRegisteredTarget(target ExecutionSelection) error {
	if err := target.Validate(); err != nil {
		return err
	}
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	r, err := s.HostRegistry()
	if err != nil {
		return err
	}
	for i, h := range r.Hosts {
		preparing := h.Target.Root == "" && h.Target.Host == target.Host && h.Target.User == target.User && h.Target.System == target.System
		if sameHostInstallation(h.Target, target) || preparing {
			r.Hosts[i].freezeConnectionNamespace()
			r.Hosts[i].Target = target
			r.Default = h.ID
			return s.saveHostRegistry(r)
		}
	}
	name := suggestedHostName(target)
	base := name
	for n := 2; ; n++ {
		if _, err := r.Find(name); err != nil {
			break
		}
		name = fmt.Sprintf("%s-%d", base, n)
	}
	id, err := newHostRecordID()
	if err != nil {
		return err
	}
	h := HostRecord{ID: id, Name: name, Target: target}
	r.Hosts = append(r.Hosts, h)
	r.Default = h.ID
	return s.saveHostRegistry(r)
}

// UpdateHostTarget prepares a registered target without changing the CLI default.
func (s Store) UpdateHostTarget(id string, previous, next ExecutionSelection) error {
	if err := next.Validate(); err != nil {
		return err
	}
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	r, err := s.HostRegistry()
	if err != nil {
		return err
	}
	for i, h := range r.Hosts {
		if h.ID == id {
			if h.Target != previous {
				return fmt.Errorf("registered host changed during preparation")
			}
			r.Hosts[i].freezeConnectionNamespace()
			r.Hosts[i].Target = next
			return s.saveHostRegistry(r)
		}
	}
	return fmt.Errorf("registered host was removed during preparation")
}
func (s Store) RemoveHost(name string) error {
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	r, err := s.HostRegistry()
	if err != nil {
		return err
	}
	h, err := r.Find(name)
	if err != nil {
		return err
	}
	if h.Target.Host.Kind == "local" && !h.Target.System {
		return fmt.Errorf("the native frontend host cannot be removed")
	}
	if r.Default == h.ID {
		for _, entry := range r.Hosts {
			if entry.Target.Host.Kind == "local" && !entry.Target.System {
				r.Default = entry.ID
			}
		}
		if r.Default == h.ID {
			return fmt.Errorf("choose another default host first")
		}
	}
	next := r.Hosts[:0]
	for _, entry := range r.Hosts {
		if entry.ID != h.ID {
			next = append(next, entry)
		}
	}
	r.Hosts = next
	return s.saveHostRegistry(r)
}

// ConnectionNamespace preserves an existing tunnel's credentials while new
// connections use immutable host IDs instead of transport aliases.
func (h HostRecord) ConnectionNamespace() string {
	if h.LegacyNamespace != "" {
		return h.LegacyNamespace
	}
	if h.LegacyConnection {
		if h.Target.Host.Kind == "wsl" {
			return h.Target.Host.Distribution
		}
		if h.Target.Host.Kind == "ssh" {
			sum := sha256.Sum256([]byte(h.Target.Host.Address + "\x00" + h.Target.Root))
			return fmt.Sprintf("ssh-%x", sum[:8])
		}
	}
	return "host-" + h.ID
}

func (s Store) EnsureRegisteredHost(target ExecutionSelection) (HostRecord, error) {
	if h, err := s.HostForTarget(target); err == nil {
		return h, nil
	}
	r, err := s.HostRegistry()
	if err != nil {
		return HostRecord{}, err
	}
	name := suggestedHostName(target)
	base := name
	for n := 2; ; n++ {
		if _, err := r.Find(name); err != nil {
			break
		}
		name = fmt.Sprintf("%s-%d", base, n)
	}
	h, err := s.RegisterHost(name, target)
	if err != nil {
		if existing, e := s.HostForTarget(target); e == nil {
			return existing, nil
		}
	}
	return h, err
}

func newHostRecordID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func sameExecutionEnvironment(a, b tools.Host) bool {
	if a.Kind == "wsl" && b.Kind == "wsl" {
		return strings.EqualFold(a.Distribution, b.Distribution)
	}
	return a == b
}

func (h *HostRecord) freezeConnectionNamespace() {
	if h.LegacyConnection && h.Target.Host.Kind == "ssh" && h.LegacyNamespace == "" {
		h.LegacyNamespace = h.ConnectionNamespace()
	}
}
