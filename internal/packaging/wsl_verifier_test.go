package packaging

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestWSLShellScriptsParse(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, relative := range []string{
		"packaging/wsl/provision.sh",
		"scripts/build/build-wsl.sh",
		"scripts/verify/verify-wsl.sh",
	} {
		command := exec.Command("/bin/sh", "-n", filepath.Join(root, filepath.FromSlash(relative)))
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("%s shell syntax: %v: %s", relative, err, output)
		}
	}
}

func TestWSLVerifierAcceptsSyntheticContractArchive(t *testing.T) {
	repo := filepath.Join("..", "..")
	root := t.TempDir()
	write := func(relative string, mode os.FileMode, content string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	mkdir := func(relative string, mode os.FileMode) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(path, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}

	write("etc/wsl.conf", 0644, "[boot]\nsystemd=true\n\n[user]\ndefault=ubuntu\n")
	write("etc/wsl-distribution.conf", 0644, "[oobe]\ndefaultUid=1000\ndefaultName=loki-mcp\n")
	write("etc/passwd", 0644, "root:x:0:0:root:/root:/bin/bash\nubuntu:x:1000:1000:ubuntu:/home/ubuntu:/bin/bash\n")
	write("etc/group", 0644, "root:x:0:\nubuntu:x:1000:\ndocker:x:999:\n")
	write("etc/shadow", 0600, "root:*:20000:0:99999:7:::\nubuntu:!:20000:0:99999:7:::\n")
	write("usr/lib/loki-appliance/loki", 0755, "fixture-host-binary\n")
	write("usr/lib/loki-appliance/release-manifest.json", 0600, "{\"fixture\":true}\n")
	write("usr/lib/loki-appliance/provision", 0755, "#!/bin/sh\nhost install --system --workspace \"$workspace\"\nhost doctor --system\n")
	write("usr/lib/systemd/system/loki-appliance-provision.service", 0644, "[Service]\nType=oneshot\n")
	mkdir("home/ubuntu/workspace", 0750)

	mkdir("etc/systemd/system/multi-user.target.wants", 0755)
	for _, link := range []struct{ path, target string }{
		{"etc/systemd/system/multi-user.target.wants/docker.service", "/usr/lib/systemd/system/docker.service"},
		{"etc/systemd/system/multi-user.target.wants/loki-appliance-provision.service", "/usr/lib/systemd/system/loki-appliance-provision.service"},
	} {
		if err := os.Symlink(link.target, filepath.Join(root, filepath.FromSlash(link.path))); err != nil {
			t.Fatal(err)
		}
	}
	mkdir("etc/systemd/system", 0755)
	for _, unit := range []string{
		"systemd-resolved.service",
		"systemd-networkd.service",
		"NetworkManager.service",
		"systemd-tmpfiles-setup.service",
		"systemd-tmpfiles-clean.service",
		"systemd-tmpfiles-clean.timer",
		"systemd-tmpfiles-setup-dev-early.service",
		"systemd-tmpfiles-setup-dev.service",
		"tmp.mount",
	} {
		if err := os.Symlink("/dev/null", filepath.Join(root, "etc", "systemd", "system", unit)); err != nil {
			t.Fatal(err)
		}
	}

	host := filepath.Join(t.TempDir(), "loki-linux-amd64")
	manifest := filepath.Join(t.TempDir(), "release-manifest.json")
	if err := copyTestFile(filepath.Join(root, "usr/lib/loki-appliance/loki"), host, 0755); err != nil {
		t.Fatal(err)
	}
	if err := copyTestFile(filepath.Join(root, "usr/lib/loki-appliance/release-manifest.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(t.TempDir(), "fixture.wsl")
	if err := writeWSLTestArchive(root, archive); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/bin/sh", filepath.Join(repo, "scripts", "verify", "verify-wsl.sh"), archive, host, manifest)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("verify synthetic WSL archive: %v\n%s", err, output)
	}
}

func copyTestFile(source, destination string, mode os.FileMode) error {
	raw, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, raw, mode)
}

func writeWSLTestArchive(root, destination string) error {
	output, err := os.Create(destination)
	if err != nil {
		return err
	}
	gz, err := gzip.NewWriterLevel(output, gzip.BestCompression)
	if err != nil {
		_ = output.Close()
		return err
	}
	tw := tar.NewWriter(gz)

	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		header.Uid = 0
		header.Gid = 0
		header.ModTime = time.Unix(0, 0).UTC()
		if header.Name == "home/ubuntu/workspace" {
			header.Uid = 1000
			header.Gid = 1000
		}
		if info.IsDir() {
			header.Name += "/"
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	closeTar := tw.Close()
	closeGzip := gz.Close()
	closeOutput := output.Close()
	if err != nil {
		return err
	}
	if closeTar != nil {
		return closeTar
	}
	if closeGzip != nil {
		return closeGzip
	}
	return closeOutput
}
