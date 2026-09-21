package releases

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

type fixtureKey struct {
	tuf    *metadata.Key
	id     string
	signer signature.Signer
}

type repositoryFixture struct {
	baseURL       string
	rootKeys      []fixtureKey
	targetsKey    fixtureKey
	snapshotKey   fixtureKey
	timestampKey  fixtureKey
	releaseKey    fixtureKey
	toolchainKey  fixtureKey
	bootstrapRoot []byte
}

type repositoryOptions struct {
	timestampExpires  time.Time
	snapshotExpires   time.Time
	targetsExpires    time.Time
	releaseExpires    time.Time
	toolchainExpires  time.Time
	wrongReleaseKey   bool
	wrongToolchainKey bool
	corruptRole       string
}

type memoryFetcher struct {
	files map[string][]byte
}

func (f *memoryFetcher) DownloadFile(urlPath string, maxLength int64, _ time.Duration) ([]byte, error) {
	raw, ok := f.files[urlPath]
	if !ok {
		return nil, &metadata.ErrDownloadHTTP{StatusCode: http.StatusNotFound, URL: urlPath}
	}
	if maxLength > 0 && int64(len(raw)) > maxLength {
		return nil, fmt.Errorf("fixture metadata exceeds requested limit")
	}
	return append([]byte(nil), raw...), nil
}

func TestClientResolvesRoleScopedTargets(t *testing.T) {
	now := time.Now().UTC()
	fixture := newRepositoryFixture(t, now)
	repo := fixture.repository(t, 1, now.Add(24*time.Hour))
	stateRoot := privateReleaseStateRoot(t)
	client := openFixtureClient(t, fixture, repo, stateRoot)

	releaseData := []byte("release-manifest\n")
	release, err := client.ResolveRelease(t.Context(), "loki-1.2.3.json")
	if err != nil {
		t.Fatal(err)
	}
	assertDescriptor(t, release, "releases/loki-1.2.3.json", releaseData)
	if err = release.VerifyBytes(releaseData); err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), releaseData...)
	tampered[0] ^= 0x20
	if err = release.VerifyBytes(tampered); err == nil {
		t.Fatal("same-length target with the wrong digest was accepted")
	}
	if err = release.VerifyBytes([]byte("short")); err == nil {
		t.Fatal("target with the wrong length was accepted")
	}

	toolchainData := []byte("toolchain-catalog\n")
	toolchain, err := client.ResolveToolchain(t.Context(), "catalog-v7.json")
	if err != nil {
		t.Fatal(err)
	}
	assertDescriptor(t, toolchain, "toolchains/catalog-v7.json", toolchainData)

	for _, name := range []string{"metadata", "targets"} {
		info, statErr := os.Stat(filepath.Join(stateRoot, name))
		if statErr != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("%s cache mode = %v, %v", name, info, statErr)
		}
	}
	if _, err = client.ResolveRelease(t.Context(), "../toolchains/catalog-v7.json"); err == nil {
		t.Fatal("cross-namespace traversal was accepted")
	}
}

func TestClientRejectsForgedMetadata(t *testing.T) {
	now := time.Now().UTC()
	fixture := newRepositoryFixture(t, now)
	expires := now.Add(24 * time.Hour)

	tests := []struct {
		role    string
		resolve string
	}{
		{role: "timestamp"},
		{role: "snapshot"},
		{role: "targets"},
		{role: "releases", resolve: "release"},
		{role: "toolchains", resolve: "toolchain"},
	}
	for _, tc := range tests {
		t.Run(tc.role, func(t *testing.T) {
			options := uniformRepositoryOptions(expires)
			options.corruptRole = tc.role
			repo := fixture.repositoryWithOptions(t, 1, options)
			client := openFixtureClient(t, fixture, repo, privateReleaseStateRoot(t))
			var err error
			switch tc.resolve {
			case "release":
				_, err = client.ResolveRelease(t.Context(), "loki-1.2.3.json")
			case "toolchain":
				_, err = client.ResolveToolchain(t.Context(), "catalog-v7.json")
			default:
				err = client.Refresh(t.Context())
			}
			if err == nil {
				t.Fatalf("forged %s metadata was accepted", tc.role)
			}
		})
	}

	t.Run("root", func(t *testing.T) {
		repo := fixture.repository(t, 1, expires)
		root := fixture.rotatedRoot(t, now, 2, true)
		repo.files[fixture.baseURL+"/2.root.json"] = corruptMetadataSignature(t, root)
		client := openFixtureClient(t, fixture, repo, privateReleaseStateRoot(t))
		if err := client.Refresh(t.Context()); err == nil {
			t.Fatal("forged root metadata was accepted")
		}
	})
}

func TestClientRejectsExpiredMetadata(t *testing.T) {
	now := time.Now().UTC()
	fixture := newRepositoryFixture(t, now)
	future := now.Add(24 * time.Hour)
	past := now.Add(-time.Hour)

	tests := []struct {
		role    string
		mutate  func(*repositoryOptions)
		resolve string
	}{
		{role: "timestamp", mutate: func(o *repositoryOptions) { o.timestampExpires = past }},
		{role: "snapshot", mutate: func(o *repositoryOptions) { o.snapshotExpires = past }},
		{role: "targets", mutate: func(o *repositoryOptions) { o.targetsExpires = past }},
		{role: "releases", mutate: func(o *repositoryOptions) { o.releaseExpires = past }, resolve: "release"},
		{role: "toolchains", mutate: func(o *repositoryOptions) { o.toolchainExpires = past }, resolve: "toolchain"},
	}
	for _, tc := range tests {
		t.Run(tc.role, func(t *testing.T) {
			options := uniformRepositoryOptions(future)
			tc.mutate(&options)
			repo := fixture.repositoryWithOptions(t, 1, options)
			client := openFixtureClient(t, fixture, repo, privateReleaseStateRoot(t))
			var err error
			switch tc.resolve {
			case "release":
				_, err = client.ResolveRelease(t.Context(), "loki-1.2.3.json")
			case "toolchain":
				_, err = client.ResolveToolchain(t.Context(), "catalog-v7.json")
			default:
				err = client.Refresh(t.Context())
			}
			if err == nil {
				t.Fatalf("expired %s metadata was accepted", tc.role)
			}
		})
	}
}

func TestClientRejectsWrongDelegatedRoleKeys(t *testing.T) {
	now := time.Now().UTC()
	fixture := newRepositoryFixture(t, now)
	expires := now.Add(24 * time.Hour)

	t.Run("release-signed-by-toolchain-key", func(t *testing.T) {
		options := uniformRepositoryOptions(expires)
		options.wrongReleaseKey = true
		client := openFixtureClient(t, fixture, fixture.repositoryWithOptions(t, 1, options), privateReleaseStateRoot(t))
		if _, err := client.ResolveRelease(t.Context(), "loki-1.2.3.json"); err == nil {
			t.Fatal("release metadata signed with the toolchain key was accepted")
		}
	})

	t.Run("toolchain-signed-by-release-key", func(t *testing.T) {
		options := uniformRepositoryOptions(expires)
		options.wrongToolchainKey = true
		client := openFixtureClient(t, fixture, fixture.repositoryWithOptions(t, 1, options), privateReleaseStateRoot(t))
		if _, err := client.ResolveToolchain(t.Context(), "catalog-v7.json"); err == nil {
			t.Fatal("toolchain metadata signed with the release key was accepted")
		}
	})
}

func TestClientRejectsSnapshotMixAndMatch(t *testing.T) {
	now := time.Now().UTC()
	fixture := newRepositoryFixture(t, now)
	repo := fixture.repository(t, 1, now.Add(24*time.Hour))
	key := fixture.baseURL + "/1.snapshot.json"
	repo.files[key] = append(append([]byte(nil), repo.files[key]...), ' ')
	client := openFixtureClient(t, fixture, repo, privateReleaseStateRoot(t))
	if err := client.Refresh(t.Context()); err == nil {
		t.Fatal("snapshot content inconsistent with timestamp metadata was accepted")
	}
}

func TestClientRejectsRollbackAcrossRestart(t *testing.T) {
	now := time.Now().UTC()
	fixture := newRepositoryFixture(t, now)
	stateRoot := privateReleaseStateRoot(t)

	newer := fixture.repository(t, 2, now.Add(24*time.Hour))
	client := openFixtureClient(t, fixture, newer, stateRoot)
	if err := client.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}

	older := fixture.repository(t, 1, now.Add(24*time.Hour))
	client = openFixtureClient(t, fixture, older, stateRoot)
	if err := client.Refresh(t.Context()); err == nil {
		t.Fatal("older timestamp metadata was accepted after trusted version state was persisted")
	}
}

func TestClientRootRotationRecovery(t *testing.T) {
	now := time.Now().UTC()
	fixture := newRepositoryFixture(t, now)

	t.Run("sequential-recovery", func(t *testing.T) {
		repo := fixture.repository(t, 1, now.Add(24*time.Hour))
		repo.files[fixture.baseURL+"/2.root.json"] = fixture.rotatedRoot(t, now, 2, true)
		stateRoot := privateReleaseStateRoot(t)
		client := openFixtureClient(t, fixture, repo, stateRoot)
		if err := client.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(stateRoot, "metadata", "root.json"))
		if err != nil {
			t.Fatal(err)
		}
		root, err := parseRoot(raw)
		if err != nil {
			t.Fatal(err)
		}
		if root.Signed.Version != 2 {
			t.Fatalf("trusted root version = %d, want 2", root.Signed.Version)
		}

		withoutOldRoot := fixture.repository(t, 1, now.Add(24*time.Hour))
		client = openFixtureClient(t, fixture, withoutOldRoot, stateRoot)
		if err = client.Refresh(t.Context()); err != nil {
			t.Fatalf("persisted rotated root was not reusable: %v", err)
		}
	})

	t.Run("missing-old-threshold", func(t *testing.T) {
		repo := fixture.repository(t, 1, now.Add(24*time.Hour))
		repo.files[fixture.baseURL+"/2.root.json"] = fixture.rotatedRoot(t, now, 2, false)
		client := openFixtureClient(t, fixture, repo, privateReleaseStateRoot(t))
		if err := client.Refresh(t.Context()); err == nil {
			t.Fatal("root rotation without the old trust threshold was accepted")
		}
	})

	t.Run("skipped-root-version", func(t *testing.T) {
		repo := fixture.repository(t, 1, now.Add(24*time.Hour))
		repo.files[fixture.baseURL+"/2.root.json"] = fixture.rotatedRoot(t, now, 3, true)
		client := openFixtureClient(t, fixture, repo, privateReleaseStateRoot(t))
		if err := client.Refresh(t.Context()); err == nil {
			t.Fatal("root metadata that skipped the requested version was accepted")
		}
	})
}

func TestOpenRejectsUnsafeTrustState(t *testing.T) {
	now := time.Now().UTC()
	fixture := newRepositoryFixture(t, now)

	publicRoot := filepath.Join(t.TempDir(), "release-state")
	if err := os.Mkdir(publicRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(publicRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Config{
		StateRoot:         publicRoot,
		RemoteMetadataURL: fixture.baseURL,
		TrustedRoot:       fixture.bootstrapRoot,
	}); err == nil {
		t.Fatal("public trust-state directory was accepted")
	}
	if _, err := Open(Config{
		StateRoot:         privateReleaseStateRoot(t),
		RemoteMetadataURL: "http://updates.example.test/repository",
		TrustedRoot:       fixture.bootstrapRoot,
	}); err == nil {
		t.Fatal("plaintext metadata transport was accepted")
	}
}

func newRepositoryFixture(t *testing.T, now time.Time) *repositoryFixture {
	t.Helper()
	fixture := &repositoryFixture{baseURL: "https://updates.example.test/repository"}
	for range 3 {
		fixture.rootKeys = append(fixture.rootKeys, newFixtureKey(t))
	}
	fixture.targetsKey = newFixtureKey(t)
	fixture.snapshotKey = newFixtureKey(t)
	fixture.timestampKey = newFixtureKey(t)
	fixture.releaseKey = newFixtureKey(t)
	fixture.toolchainKey = newFixtureKey(t)

	root := metadata.Root(now.Add(365 * 24 * time.Hour))
	for _, key := range fixture.rootKeys {
		if err := root.Signed.AddKey(key.tuf, metadata.ROOT); err != nil {
			t.Fatal(err)
		}
	}
	root.Signed.Roles[metadata.ROOT].Threshold = 2
	for role, key := range map[string]fixtureKey{
		metadata.TARGETS:   fixture.targetsKey,
		metadata.SNAPSHOT:  fixture.snapshotKey,
		metadata.TIMESTAMP: fixture.timestampKey,
	} {
		if err := root.Signed.AddKey(key.tuf, role); err != nil {
			t.Fatal(err)
		}
	}
	signMetadata(t, root, fixture.rootKeys[0], fixture.rootKeys[1])
	fixture.bootstrapRoot = metadataBytes(t, root)
	return fixture
}

func uniformRepositoryOptions(expires time.Time) repositoryOptions {
	return repositoryOptions{
		timestampExpires: expires,
		snapshotExpires:  expires,
		targetsExpires:   expires,
		releaseExpires:   expires,
		toolchainExpires: expires,
	}
}

func (f *repositoryFixture) repository(t *testing.T, version int64, expires time.Time) *memoryFetcher {
	t.Helper()
	return f.repositoryWithOptions(t, version, uniformRepositoryOptions(expires))
}

func (f *repositoryFixture) repositoryWithOptions(t *testing.T, version int64, options repositoryOptions) *memoryFetcher {
	t.Helper()
	releasePath := "releases/loki-1.2.3.json"
	releaseData := []byte("release-manifest\n")
	toolchainPath := "toolchains/catalog-v7.json"
	toolchainData := []byte("toolchain-catalog\n")

	releases := metadata.Targets(options.releaseExpires)
	releases.Signed.Version = version
	releases.Signed.Targets[releasePath] = targetInfo(t, releasePath, releaseData)
	if options.wrongReleaseKey {
		signMetadata(t, releases, f.toolchainKey)
	} else {
		signMetadata(t, releases, f.releaseKey)
	}
	releasesRaw := metadataBytes(t, releases)
	if options.corruptRole == "releases" {
		releasesRaw = corruptMetadataSignature(t, releasesRaw)
	}

	toolchains := metadata.Targets(options.toolchainExpires)
	toolchains.Signed.Version = version
	toolchains.Signed.Targets[toolchainPath] = targetInfo(t, toolchainPath, toolchainData)
	if options.wrongToolchainKey {
		signMetadata(t, toolchains, f.releaseKey)
	} else {
		signMetadata(t, toolchains, f.toolchainKey)
	}
	toolchainsRaw := metadataBytes(t, toolchains)
	if options.corruptRole == "toolchains" {
		toolchainsRaw = corruptMetadataSignature(t, toolchainsRaw)
	}

	targets := metadata.Targets(options.targetsExpires)
	targets.Signed.Version = version
	targets.Signed.Delegations = &metadata.Delegations{
		Keys: map[string]*metadata.Key{
			f.releaseKey.id:   f.releaseKey.tuf,
			f.toolchainKey.id: f.toolchainKey.tuf,
		},
		Roles: []metadata.DelegatedRole{
			{
				Name:        "releases",
				KeyIDs:      []string{f.releaseKey.id},
				Threshold:   1,
				Terminating: true,
				Paths:       []string{"releases/*"},
			},
			{
				Name:        "toolchains",
				KeyIDs:      []string{f.toolchainKey.id},
				Threshold:   1,
				Terminating: true,
				Paths:       []string{"toolchains/*"},
			},
		},
	}
	signMetadata(t, targets, f.targetsKey)
	targetsRaw := metadataBytes(t, targets)
	if options.corruptRole == "targets" {
		targetsRaw = corruptMetadataSignature(t, targetsRaw)
	}

	snapshot := metadata.Snapshot(options.snapshotExpires)
	snapshot.Signed.Version = version
	snapshot.Signed.Meta["targets.json"] = metaInfo(version, targetsRaw)
	snapshot.Signed.Meta["releases.json"] = metaInfo(version, releasesRaw)
	snapshot.Signed.Meta["toolchains.json"] = metaInfo(version, toolchainsRaw)
	signMetadata(t, snapshot, f.snapshotKey)
	snapshotRaw := metadataBytes(t, snapshot)
	if options.corruptRole == "snapshot" {
		snapshotRaw = corruptMetadataSignature(t, snapshotRaw)
	}

	timestamp := metadata.Timestamp(options.timestampExpires)
	timestamp.Signed.Version = version
	timestamp.Signed.Meta["snapshot.json"] = metaInfo(version, snapshotRaw)
	signMetadata(t, timestamp, f.timestampKey)
	timestampRaw := metadataBytes(t, timestamp)
	if options.corruptRole == "timestamp" {
		timestampRaw = corruptMetadataSignature(t, timestampRaw)
	}

	return &memoryFetcher{files: map[string][]byte{
		f.baseURL + "/timestamp.json":                            timestampRaw,
		fmt.Sprintf("%s/%d.snapshot.json", f.baseURL, version):   snapshotRaw,
		fmt.Sprintf("%s/%d.targets.json", f.baseURL, version):    targetsRaw,
		fmt.Sprintf("%s/%d.releases.json", f.baseURL, version):   releasesRaw,
		fmt.Sprintf("%s/%d.toolchains.json", f.baseURL, version): toolchainsRaw,
	}}
}

func (f *repositoryFixture) rotatedRoot(t *testing.T, now time.Time, version int64, validOldThreshold bool) []byte {
	t.Helper()
	root, err := parseRoot(f.bootstrapRoot)
	if err != nil {
		t.Fatal(err)
	}
	root.ClearSignatures()
	root.Signed.Version = version

	removed := f.rootKeys[0]
	retained := f.rootKeys[1]
	replacement := newFixtureKey(t)
	if err = root.Signed.RevokeKey(removed.id, metadata.ROOT); err != nil {
		t.Fatal(err)
	}
	if err = root.Signed.AddKey(replacement.tuf, metadata.ROOT); err != nil {
		t.Fatal(err)
	}
	root.Signed.Expires = now.Add(365 * 24 * time.Hour)
	if validOldThreshold {
		signMetadata(t, root, removed, retained, replacement)
	} else {
		signMetadata(t, root, retained, replacement)
	}
	return metadataBytes(t, root)
}

func newFixtureKey(t *testing.T) fixtureKey {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := metadata.KeyFromPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	id, err := key.ID()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := signature.LoadSigner(private, crypto.Hash(0))
	if err != nil {
		t.Fatal(err)
	}
	return fixtureKey{tuf: key, id: id, signer: signer}
}

type signableMetadata interface {
	Sign(signature.Signer) (*metadata.Signature, error)
}

type byteMetadata interface {
	ToBytes(bool) ([]byte, error)
}

func signMetadata(t *testing.T, meta signableMetadata, keys ...fixtureKey) {
	t.Helper()
	for _, key := range keys {
		if _, err := meta.Sign(key.signer); err != nil {
			t.Fatal(err)
		}
	}
}

func metadataBytes(t *testing.T, meta byteMetadata) []byte {
	t.Helper()
	raw, err := meta.ToBytes(false)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func targetInfo(t *testing.T, targetPath string, data []byte) *metadata.TargetFiles {
	t.Helper()
	info, err := metadata.TargetFile().FromBytes(targetPath, data, "sha256")
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func metaInfo(version int64, raw []byte) *metadata.MetaFiles {
	sum := sha256.Sum256(raw)
	return &metadata.MetaFiles{
		Version: version,
		Length:  int64(len(raw)),
		Hashes: metadata.Hashes{
			"sha256": metadata.HexBytes(sum[:]),
		},
	}
}

func corruptMetadataSignature(t *testing.T, raw []byte) []byte {
	t.Helper()
	corrupted := append([]byte(nil), raw...)
	marker := []byte(`"sig":"`)
	index := bytes.Index(corrupted, marker)
	if index < 0 || index+len(marker) >= len(corrupted) {
		t.Fatal("fixture metadata has no signature")
	}
	index += len(marker)
	if corrupted[index] == '0' {
		corrupted[index] = '1'
	} else {
		corrupted[index] = '0'
	}
	return corrupted
}

func privateReleaseStateRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "release-state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

func openFixtureClient(t *testing.T, fixture *repositoryFixture, repo *memoryFetcher, stateRoot string) *Client {
	t.Helper()
	client, err := Open(Config{
		StateRoot:         stateRoot,
		RemoteMetadataURL: fixture.baseURL,
		TrustedRoot:       fixture.bootstrapRoot,
		Fetcher:           repo,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func assertDescriptor(t *testing.T, got TargetDescriptor, targetPath string, data []byte) {
	t.Helper()
	sum := sha256.Sum256(data)
	if got.Path != targetPath || got.Length != int64(len(data)) || got.SHA256 != fmt.Sprintf("%x", sum[:]) {
		t.Fatalf("descriptor = %#v", got)
	}
}
