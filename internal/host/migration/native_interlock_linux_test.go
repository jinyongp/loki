//go:build linux

package migration

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeBootInterlockTiedToOriginalOperation(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("native values suite runs this owned filesystem fixture as root")
	}
	root := t.TempDir()
	for _, dir := range []string{"etc/systemd/system", "run"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	g := nativeBootInterlock{root: root, operation: "123e4567e89b42d3a456426614174521"}
	if err := g.allow(); err == nil {
		t.Fatal("granted start permission without persistent service conditions")
	}
	if err := g.install(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(g.permit()); !os.IsNotExist(err) {
		t.Fatal("installing the boot fence granted start permission", err)
	}
	if err := g.install(); err != nil {
		t.Fatal("lost installation ACK was not replayable", err)
	}
	foreign := g
	foreign.operation = "123e4567e89b42d3a456426614174599"
	if err := foreign.install(); err == nil {
		t.Fatal("another operation replaced the persistent fence")
	}
	if err := foreign.allow(); err == nil {
		t.Fatal("another operation granted start permission")
	}
	if err := foreign.remove(); err == nil {
		t.Fatal("another operation removed the persistent fence")
	}
	if err := g.allow(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "run/loki-go/secret-migration")); err != nil {
		t.Fatal(err)
	}
	for _, unit := range nativeMigrationUnits {
		if err := nativeInterlockFile(g.dropin(unit), g.condition(), false); err != nil {
			t.Fatal("reboot lost the persistent service fence", unit, err)
		}
	}
	if _, err := os.Lstat(g.permit()); !os.IsNotExist(err) {
		t.Fatal("volatile permission survived reboot", err)
	}
	if err := g.allow(); err != nil {
		t.Fatal("original recovery could not grant serving permission", err)
	}
	if err := g.remove(); err != nil {
		t.Fatal(err)
	}
	if err := g.remove(); err != nil {
		t.Fatal("lost fence-removal ACK was not replayable", err)
	}
	path := g.dropin(nativeMigrationUnits[0])
	outside := filepath.Join(root, "foreign.conf")
	if err := os.WriteFile(outside, g.condition(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if err := g.install(); err == nil {
		t.Fatal("followed a symlink instead of retaining foreign state")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("foreign state was modified", err)
	}
}
