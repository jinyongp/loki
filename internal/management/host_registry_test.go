package management

import (
	"loki/internal/tools"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func registryTarget(root string) ExecutionSelection {
	return ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "wsl", Distribution: "Ubuntu"}, User: "alice", Root: root, Command: "loki"}
}
func TestRegistryLegacyImportIsReadOnlyAndPersistsIdentities(t *testing.T) {
	store := Store{Root: t.TempDir()}
	if err := os.MkdirAll(store.ControlDirectory(), 0700); err != nil {
		t.Fatal(err)
	}
	target := registryTarget("/home/alice/.config/loki")
	if err := atomicJSON(filepath.Join(store.ControlDirectory(), "execution-host.json"), target); err != nil {
		t.Fatal(err)
	}
	r, err := store.HostRegistry()
	if err != nil {
		t.Fatal(err)
	}
	before := r.Selected()
	if before.Target != target || !before.LegacyConnection || len(r.Hosts) != 2 {
		t.Fatalf("incorrect import: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(store.ControlDirectory(), "hosts.json")); !os.IsNotExist(err) {
		t.Fatal("read migrated state on disk", err)
	}
	if err := store.RenameHost(before.ID, "daily"); err != nil {
		t.Fatal(err)
	}
	if err := store.UseHost("local"); err != nil {
		t.Fatal(err)
	}
	after, err := store.RegisteredHost("daily")
	if err != nil || after.ID != before.ID || after.Target != target || after.ConnectionNamespace() != before.ConnectionNamespace() {
		t.Fatal("identity changed", after, err)
	}
	if selected, err := store.ExecutionSelection(); err != nil || selected == nil || selected.Host.Kind != "local" {
		t.Fatal("CLI default did not change", selected, err)
	}
}
func TestRegistrySeparatesInstallationsAndDoesNotReuseRemovedIDs(t *testing.T) {
	store := Store{Root: t.TempDir()}
	a, err := store.RegisterHost("work", registryTarget("/home/alice/work"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.RegisterHost("test", registryTarget("/home/alice/test"))
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || a.ConnectionNamespace() == b.ConnectionNamespace() {
		t.Fatal("installations collapsed")
	}
	if _, err := store.RegisterHost("duplicate", a.Target); err == nil {
		t.Fatal("duplicate accepted")
	}
	target := a.Target
	target.User = "bob"
	if _, err := store.RegisterHost("bob", target); err != nil {
		t.Fatal("different user rejected", err)
	}
	if err := store.UseHost("work"); err != nil {
		t.Fatal(err)
	}
	next := a.Target
	next.Command = "/home/alice/.local/bin/loki"
	if err := store.UpdateHostTarget(a.ID, a.Target, next); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateHostTarget(a.ID, a.Target, next); err == nil {
		t.Fatal("stale preparation accepted")
	}
	if err := store.RemoveHost("work"); err != nil {
		t.Fatal(err)
	}
	c, err := store.RegisterHost("work", a.Target)
	if err != nil {
		t.Fatal(err)
	}
	if c.ID == a.ID {
		t.Fatal("removed connection could target a new registration")
	}
	if r, err := store.HostRegistry(); err != nil || r.Selected().Name != "local" {
		t.Fatal("removing default did not select native", r, err)
	}
	if err := store.RemoveHost("local"); err == nil {
		t.Fatal("native host removed")
	}
}
func TestMultipleHostPreparationsRetainOwnershipIndependently(t *testing.T) {
	store := Store{Root: t.TempDir()}
	for _, name := range []string{"loki-tools-one", "loki-tools-two"} {
		if err := store.SaveHostPreparation(HostPreparation{Schema: 1, Distribution: name, Token: strings.Repeat("a", 64), Phase: "reserved"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ForgetHostPreparation("loki-tools-one"); err != nil {
		t.Fatal(err)
	}
	if p, err := store.HostPreparationFor("loki-tools-two"); err != nil || p == nil {
		t.Fatal("other ownership lost", err)
	}
	if p, err := store.HostPreparationFor("loki-tools-one"); err != nil || p != nil {
		t.Fatal("removed preparation resurrected", err)
	}
}

func TestHostIDsCannotBeShadowedByNames(t *testing.T) {
 id:=strings.Repeat("a",32)
 r:=HostRegistry{Hosts:[]HostRecord{{ID:strings.Repeat("b",32),Name:id},{ID:id,Name:"target"}}}
 h,err:=r.Find(id);if err!=nil || h.Name!="target"{t.Fatal("immutable ID resolved as another host's name",h,err)}
 if err:=ValidateHostName(id);err==nil{t.Fatal("ID-shaped host name accepted")}
 store:=Store{Root:t.TempDir()};if err:=store.RenameHost("local","desktop");err!=nil{t.Fatal(err)}
 if _,err:=store.RegisterHost("local",registryTarget("/remote"));err==nil{t.Fatal("raw native transport name assigned to a remote host")}
}
func TestLegacyTunnelNamespaceSurvivesPreparation(t *testing.T) {
 store:=Store{Root:t.TempDir()};if err:=os.MkdirAll(store.ControlDirectory(),0700);err!=nil{t.Fatal(err)}
 target:=ExecutionSelection{Schema:1,Host:tools.Host{Kind:"ssh",Address:"user@host"},Command:"loki"}
 if err:=atomicJSON(filepath.Join(store.ControlDirectory(),"execution-host.json"),target);err!=nil{t.Fatal(err)}
 registry,err:=store.HostRegistry();if err!=nil{t.Fatal(err)};before:=registry.Selected()
 next:=target;next.Root="/home/user/.config/loki"
 if err:=store.UpdateHostTarget(before.ID,target,next);err!=nil{t.Fatal(err)}
 after,err:=store.RegisteredHost(before.ID);if err!=nil || after.ConnectionNamespace()!=before.ConnectionNamespace(){t.Fatal("preparation orphaned existing tunnel",err)}
}
