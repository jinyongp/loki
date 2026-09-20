package packaging

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"loki/internal/execution"
)

func TestOCIImageDefinesPortableRuntime(t *testing.T) {
	root := filepath.Join("..", "..")
	dockerfile := readOCIFile(t, filepath.Join(root, "packaging", "container", "Dockerfile"))
	dockerignore := readOCIFile(t, filepath.Join(root, ".dockerignore"))
	if !strings.HasPrefix(dockerignore, "**\n") || strings.Contains(dockerignore, "pyproject") || strings.Contains(dockerignore, "tests/") {
		t.Fatal("container build context is not an explicit Go allowlist")
	}
	for _, required := range []string{
		"ARG TARGETARCH",
		"ARG SOURCE_DATE_EPOCH=0",
		"GOOS=\"$TARGETOS\" GOARCH=\"$TARGETARCH\"",
		"COPY --from=artifacts --chmod=0755 /devtools/${TARGETARCH}/devtools",
		"COPY --from=artifacts --chmod=0755 /ripgrep/${TARGETARCH}/rg",
		"COPY --from=artifacts --chmod=0755 /gh/${TARGETARCH}/gh",
		"io.loki.devtools.amd64.sha256",
		"io.loki.devtools.arm64.sha256",
		"io.loki.ripgrep.amd64.sha256",
		"io.loki.ripgrep.arm64.sha256",
		"io.loki.gh.amd64.sha256",
		"io.loki.gh.arm64.sha256",
		"dockerfile:1.27.0@sha256:",
		"golang:1.27.1-bookworm@sha256:",
		"alpine:3.24.2@sha256:",
		"alpine/git:2.54.0@sha256:",
		"ENTRYPOINT [\"/opt/loki/bin/loki\"]",
		"-o /out/loki-launcher ./cmd/loki-launcher",
		"-o /out/loki-executor ./cmd/loki-executor",
		"COPY --from=build /out/loki-launcher /opt/loki/bin/loki-launcher",
		"COPY --from=build /out/loki-executor /opt/loki/bin/loki-executor",
		"runner:x:10000:10000",
		"egress:x:10002:10002",
		"executor:x:10004:10001",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("Dockerfile is missing %q", required)
		}
	}
	for _, forbidden := range []string{"python", "playwright", "docker.sock", "apt-get", "apk add"} {
		if strings.Contains(strings.ToLower(dockerfile), forbidden) {
			t.Errorf("core image contains optional or legacy dependency %q", forbidden)
		}
	}
}

func TestOptionalBrowserImageIsPortableAndPinned(t *testing.T) {
	root := filepath.Join("..", "..")
	dockerfile := readOCIFile(t, filepath.Join(root, "packaging", "container", "browser.Dockerfile"))
	for _, required := range []string{
		"ARG TARGETARCH",
		"GOOS=\"$TARGETOS\" GOARCH=\"$TARGETARCH\"",
		"chromium=$CHROMIUM_VERSION",
		"CHROMIUM_VERSION=152.0.7977.82-r0",
		"golang:1.27.1-bookworm@sha256:",
		"alpine:3.24.2@sha256:",
		"USER 10003:10003",
		"ENTRYPOINT [\"/opt/loki/bin/loki\"]",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("browser Dockerfile is missing %q", required)
		}
	}
	for _, forbidden := range []string{"playwright", "node", "python", "docker.sock"} {
		if strings.Contains(strings.ToLower(dockerfile), forbidden) {
			t.Errorf("browser image contains unrelated dependency %q", forbidden)
		}
	}
	script := readOCIFile(t, filepath.Join(root, "scripts", "build-loki-browser-oci.sh"))
	for _, required := range []string{"--platform linux/amd64,linux/arm64", "--provenance=mode=max", "type=oci,dest=$output"} {
		if !strings.Contains(script, required) {
			t.Errorf("browser build script is missing %q", required)
		}
	}
}

func TestOCIBuildValidatesDevtoolsInputsAndProvenance(t *testing.T) {
	root := filepath.Join("..", "..")
	script := readOCIFile(t, filepath.Join(root, "scripts", "build-loki-oci.sh"))
	for _, required := range []string{
		"GOARCH=amd64",
		"GOARCH=arm64",
		"GOOS=linux",
		"github.com/jinyongp/devtools/cmd/devtools",
		"sha256sum \"$devtools_amd64\"",
		"sha256sum \"$devtools_arm64\"",
		"elf_machine",
		"ripgrep_amd64_sha",
		"ripgrep_arm64_sha",
		"gh_amd64_sha",
		"gh_arm64_sha",
		"--platform linux/amd64,linux/arm64",
		"--provenance=mode=max",
		"SOURCE_DATE_EPOCH=$source_date_epoch",
		"devtools-catalog.json",
		"provenance.json",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("build script is missing %q", required)
		}
	}
}

func TestContainerExecutionContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "packaging", "container", "config", "execution-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	contract, err := execution.Load(raw)
	if err != nil {
		t.Fatal(err)
	}
	if contract.NetworkProfiles["dependency-install"].Proxy != "http://egress:18766" || contract.Environment["GIT_CONFIG_GLOBAL"] != "/usr/share/doc/loki/loki-gitconfig" {
		t.Fatal("container execution contract does not use container service paths")
	}
}

func readOCIFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

type ociDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Platform    *ociPlatform      `json:"platform,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type ociPlatform struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
}

type ociIndex struct {
	Manifests []ociDescriptor `json:"manifests"`
}

type ociManifest struct {
	Config ociDescriptor   `json:"config"`
	Layers []ociDescriptor `json:"layers"`
}

type ociConfig struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Config       struct {
		Entrypoint []string          `json:"Entrypoint"`
		Cmd        []string          `json:"Cmd"`
		WorkingDir string            `json:"WorkingDir"`
		Labels     map[string]string `json:"Labels"`
		Volumes    map[string]any    `json:"Volumes"`
	} `json:"config"`
}

type layerFile struct {
	Data     []byte
	Mode     int64
	Typeflag byte
	Linkname string
}

func TestOCIArchiveContents(t *testing.T) {
	archive := os.Getenv("LOKI_OCI_ARCHIVE")
	if archive == "" {
		t.Skip("LOKI_OCI_ARCHIVE is not set")
	}
	blobs := readOCIBlobs(t, archive)
	var root ociIndex
	decodeOCI(t, blobs["index.json"], &root)
	images, attestations := collectOCIImages(t, blobs, root.Manifests)
	if attestations != 2 {
		t.Fatalf("attestation manifests = %d, want 2", attestations)
	}
	if len(images) != 2 {
		t.Fatalf("platform images = %d, want 2", len(images))
	}

	seen := []string{}
	for _, image := range images {
		arch := image.platform.Architecture
		seen = append(seen, arch)
		assertOCIImage(t, blobs, image.manifest, arch)
	}
	slices.Sort(seen)
	if !slices.Equal(seen, []string{"amd64", "arm64"}) {
		t.Fatalf("architectures = %v", seen)
	}
	if repeat := os.Getenv("LOKI_OCI_ARCHIVE_REPEAT"); repeat != "" {
		repeatedBlobs := readOCIBlobs(t, repeat)
		var repeatedRoot ociIndex
		decodeOCI(t, repeatedBlobs["index.json"], &repeatedRoot)
		repeated, _ := collectOCIImages(t, repeatedBlobs, repeatedRoot.Manifests)
		want := map[string]string{}
		for _, image := range images {
			want[image.platform.Architecture] = image.digest
		}
		for _, image := range repeated {
			if want[image.platform.Architecture] != image.digest {
				t.Errorf("%s platform manifest is not reproducible: %s != %s", image.platform.Architecture, want[image.platform.Architecture], image.digest)
			}
			delete(want, image.platform.Architecture)
		}
		if len(want) != 0 {
			t.Errorf("repeat archive is missing platforms: %v", want)
		}
	}
}

type platformImage struct {
	platform ociPlatform
	manifest ociManifest
	digest   string
}

func collectOCIImages(t *testing.T, blobs map[string][]byte, descriptors []ociDescriptor) ([]platformImage, int) {
	t.Helper()
	var images []platformImage
	attestations := 0
	for _, descriptor := range descriptors {
		switch descriptor.MediaType {
		case "application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json":
			var index ociIndex
			decodeOCI(t, ociBlob(t, blobs, descriptor.Digest), &index)
			nested, count := collectOCIImages(t, blobs, index.Manifests)
			images = append(images, nested...)
			attestations += count
		case "application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json":
			if descriptor.Annotations["vnd.docker.reference.type"] == "attestation-manifest" {
				attestations++
				continue
			}
			if descriptor.Platform == nil || descriptor.Platform.OS != "linux" {
				t.Fatalf("image manifest has no Linux platform: %#v", descriptor)
			}
			var manifest ociManifest
			decodeOCI(t, ociBlob(t, blobs, descriptor.Digest), &manifest)
			images = append(images, platformImage{*descriptor.Platform, manifest, descriptor.Digest})
		default:
			t.Fatalf("unsupported OCI descriptor media type %q", descriptor.MediaType)
		}
	}
	return images, attestations
}

func assertOCIImage(t *testing.T, blobs map[string][]byte, manifest ociManifest, arch string) {
	t.Helper()
	var config ociConfig
	decodeOCI(t, ociBlob(t, blobs, manifest.Config.Digest), &config)
	if config.OS != "linux" || config.Architecture != arch ||
		!slices.Equal(config.Config.Entrypoint, []string{"/opt/loki/bin/loki"}) ||
		!slices.Equal(config.Config.Cmd, []string{"version"}) || config.Config.WorkingDir != "/workspace" {
		t.Fatalf("%s config = %#v", arch, config)
	}
	if len(config.Config.Volumes) != 0 {
		t.Fatalf("%s image declares writable volumes: %v", arch, config.Config.Volumes)
	}
	if config.Config.Labels["io.loki.devtools.version"] == "" || config.Config.Labels["io.loki.ripgrep.version"] == "" ||
		config.Config.Labels["org.opencontainers.image.revision"] == "" {
		t.Fatalf("%s provenance labels are incomplete", arch)
	}

	wanted := map[string]bool{
		"bin/sh": true, "etc/group": true, "etc/passwd": true,
		"etc/ssl/certs/ca-certificates.crt": true,
		"opt/loki/bin/devtools":             true, "opt/loki/bin/loki": true,
		"usr/bin/git": true, "usr/bin/rg": true, "usr/bin/ssh": true,
		"usr/local/bin/devtools": true, "usr/local/bin/loki": true,
		"usr/share/doc/loki/devtools-catalog.json": true,
		"usr/share/doc/loki/provenance.json":       true,
	}
	files := readLayerFiles(t, blobs, manifest.Layers, wanted)
	for path := range wanted {
		if _, ok := files[path]; !ok {
			t.Errorf("%s image is missing /%s", arch, path)
		}
	}
	if t.Failed() {
		return
	}
	for _, path := range []string{"opt/loki/bin/devtools", "opt/loki/bin/loki", "usr/bin/git", "usr/bin/rg", "usr/bin/ssh"} {
		if files[path].Mode&0111 == 0 {
			t.Errorf("%s /%s is not executable", arch, path)
		}
	}
	if files["usr/local/bin/loki"].Typeflag != tar.TypeSymlink || files["usr/local/bin/loki"].Linkname != "/opt/loki/bin/loki" ||
		files["usr/local/bin/devtools"].Typeflag != tar.TypeSymlink || files["usr/local/bin/devtools"].Linkname != "/opt/loki/libexec/devtools" {
		t.Errorf("%s command links are invalid", arch)
	}
	if !bytes.Contains(files["etc/passwd"].Data, []byte("runner:x:10000:10000:")) ||
		!bytes.Contains(files["etc/passwd"].Data, []byte("egress:x:10002:10002:")) ||
		!bytes.Contains(files["etc/group"].Data, []byte("workspace:x:10001:runner")) {
		t.Errorf("%s service identities are invalid", arch)
	}
	wantMachine := uint16(62)
	if arch == "arm64" {
		wantMachine = 183
	}
	for _, path := range []string{"opt/loki/bin/devtools", "opt/loki/bin/loki", "usr/bin/rg"} {
		if got := elfMachine(files[path].Data); got != wantMachine {
			t.Errorf("%s /%s ELF machine = %d, want %d", arch, path, got, wantMachine)
		}
	}

	type binaryChecksum struct {
		SHA256 string `json:"sha256"`
	}
	type binarySet struct {
		AMD64 binaryChecksum `json:"amd64"`
		ARM64 binaryChecksum `json:"arm64"`
	}
	var provenance struct {
		Binaries struct {
			Devtools binarySet `json:"devtools"`
			Ripgrep  binarySet `json:"ripgrep"`
		} `json:"binaries"`
	}
	decodeOCI(t, files["usr/share/doc/loki/provenance.json"].Data, &provenance)
	for name, path := range map[string]string{"devtools": "opt/loki/bin/devtools", "ripgrep": "usr/bin/rg"} {
		set := provenance.Binaries.Devtools
		if name == "ripgrep" {
			set = provenance.Binaries.Ripgrep
		}
		want := set.AMD64.SHA256
		if arch == "arm64" {
			want = set.ARM64.SHA256
		}
		got := sha256.Sum256(files[path].Data)
		if want == "" || hex.EncodeToString(got[:]) != want {
			t.Errorf("%s %s provenance checksum mismatch", arch, name)
		}
	}
}

func readOCIBlobs(t *testing.T, path string) map[string][]byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader := tar.NewReader(file)
	blobs := map[string][]byte{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		blobs[strings.TrimPrefix(header.Name, "./")] = data
	}
	return blobs
}

func readLayerFiles(t *testing.T, blobs map[string][]byte, layers []ociDescriptor, wanted map[string]bool) map[string]layerFile {
	t.Helper()
	files := map[string]layerFile{}
	for _, layer := range layers {
		data := ociBlob(t, blobs, layer.Digest)
		var reader io.Reader = bytes.NewReader(data)
		if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
			compressed, err := gzip.NewReader(reader)
			if err != nil {
				t.Fatal(err)
			}
			defer compressed.Close()
			reader = compressed
		}
		archive := tar.NewReader(reader)
		for {
			header, err := archive.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			name := strings.TrimPrefix(strings.TrimPrefix(header.Name, "./"), "/")
			if !wanted[name] {
				continue
			}
			contents, err := io.ReadAll(archive)
			if err != nil {
				t.Fatal(err)
			}
			files[name] = layerFile{contents, header.Mode, header.Typeflag, header.Linkname}
		}
	}
	return files
}

func ociBlob(t *testing.T, blobs map[string][]byte, digest string) []byte {
	t.Helper()
	algorithm, value, ok := strings.Cut(digest, ":")
	if !ok || algorithm != "sha256" {
		t.Fatalf("invalid OCI digest %q", digest)
	}
	data, ok := blobs["blobs/sha256/"+value]
	if !ok {
		t.Fatalf("missing OCI blob %q", digest)
	}
	return data
}

func decodeOCI(t *testing.T, raw []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatal(err)
	}
}

func elfMachine(data []byte) uint16 {
	if len(data) < 20 || !bytes.Equal(data[:4], []byte{0x7f, 'E', 'L', 'F'}) || data[5] != 1 {
		return 0
	}
	return binary.LittleEndian.Uint16(data[18:20])
}

func TestOCIArchiveEnvironmentPathIsAbsolute(t *testing.T) {
	if path := os.Getenv("LOKI_OCI_ARCHIVE"); path != "" && !filepath.IsAbs(path) {
		t.Fatal(fmt.Errorf("LOKI_OCI_ARCHIVE must be absolute: %s", path))
	}
}
