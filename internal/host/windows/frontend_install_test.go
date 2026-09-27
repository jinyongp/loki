package windows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeFrontendPlatform struct {
	files           map[string][]byte
	dirs            map[string]bool
	userPath        string
	publishFailures int
	publishCalls    int
	pathWrites      int
	ensureCalls     int
	sleeps          int
}

func newFakeFrontendPlatform() *fakeFrontendPlatform {
	return &fakeFrontendPlatform{files: map[string][]byte{}, dirs: map[string]bool{}}
}

func (platform *fakeFrontendPlatform) Lstat(path string) (StatePath, error) {
	if platform.dirs[path] {
		return StatePath{Exists: true, Directory: true}, nil
	}
	if _, ok := platform.files[path]; ok {
		return StatePath{Exists: true, Regular: true}, nil
	}
	return StatePath{}, nil
}

func (platform *fakeFrontendPlatform) ReadFile(path string) ([]byte, error) {
	raw, ok := platform.files[path]
	if !ok {
		return nil, errors.New("missing file")
	}
	return append([]byte(nil), raw...), nil
}

func (platform *fakeFrontendPlatform) FileDigest(path string) (string, int64, error) {
	raw, ok := platform.files[path]
	if !ok {
		return "", 0, errors.New("missing file")
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), int64(len(raw)), nil
}

func (platform *fakeFrontendPlatform) EnsurePrivateDirectory(_ context.Context, path string) error {
	platform.ensureCalls++
	platform.dirs[path] = true
	return nil
}

func (platform *fakeFrontendPlatform) PublishExecutable(_ context.Context, source, target string) error {
	platform.publishCalls++
	if platform.publishFailures > 0 {
		platform.publishFailures--
		return errors.New("sharing violation")
	}
	raw, ok := platform.files[source]
	if !ok {
		return errors.New("missing source")
	}
	platform.files[target] = append([]byte(nil), raw...)
	return nil
}

func (platform *fakeFrontendPlatform) WriteProtectedAtomic(_ context.Context, path string, raw []byte) error {
	platform.files[path] = append([]byte(nil), raw...)
	return nil
}

func (platform *fakeFrontendPlatform) UserPath(context.Context) (string, error) {
	return platform.userPath, nil
}

func (platform *fakeFrontendPlatform) SetUserPath(_ context.Context, value string) error {
	platform.userPath = value
	platform.pathWrites++
	return nil
}

func (platform *fakeFrontendPlatform) Sleep(context.Context, time.Duration) error {
	platform.sleeps++
	return nil
}

func frontendFixture(t *testing.T) (*fakeFrontendPlatform, FrontendPaths, string, ReleaseBinding) {
	t.Helper()
	platform := newFakeFrontendPlatform()
	paths, err := ResolveFrontendPaths("C:\\Users\\alice\\AppData\\Local")
	if err != nil {
		t.Fatal(err)
	}
	source := "C:\\Temp\\loki.exe"
	platform.files[source] = []byte("frontend-v1")
	return platform, paths, source, ReleaseBinding{
		ReleaseTag: "v1.2.3", SourceRevision: strings.Repeat("a", 40),
	}
}

func TestFrontendInstallerFreshAndInterruptedRecovery(t *testing.T) {
	platform, paths, source, binding := frontendFixture(t)
	platform.userPath = "C:\\Windows\\System32;C:\\Tools"
	installer := FrontendInstaller{Platform: platform, Attempts: 3}

	result, err := installer.Install(t.Context(), source, paths, binding)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != FrontendInstalled || platform.publishCalls != 1 || !result.PathChanged {
		t.Fatalf("result=%+v publishCalls=%d", result, platform.publishCalls)
	}
	if platform.userPath != "C:\\Windows\\System32;C:\\Tools;"+paths.BinDir {
		t.Fatalf("PATH=%q", platform.userPath)
	}
	ownership, err := ParseFrontendOwnership(platform.files[paths.Ownership], paths)
	if err != nil {
		t.Fatal(err)
	}
	digest, length, _ := platform.FileDigest(paths.Binary)
	if ownership.SHA256 != digest || ownership.Length != length {
		t.Fatalf("ownership=%+v digest=%s length=%d", ownership, digest, length)
	}

	delete(platform.files, paths.Ownership)
	platform.publishCalls = 0
	platform.pathWrites = 0
	result, err = installer.Install(t.Context(), source, paths, binding)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != FrontendRepaired || platform.publishCalls != 0 || platform.pathWrites != 0 {
		t.Fatalf("recovery result=%+v publishCalls=%d pathWrites=%d", result, platform.publishCalls, platform.pathWrites)
	}
}

func TestFrontendInstallerRefusesUnverifiedReplacement(t *testing.T) {
	platform, paths, source, binding := frontendFixture(t)
	platform.files[paths.Binary] = []byte("foreign")
	_, err := (FrontendInstaller{Platform: platform}).Install(t.Context(), source, paths, binding)
	if err == nil || !strings.Contains(err.Error(), "unverified canonical") {
		t.Fatalf("err=%v", err)
	}
	if string(platform.files[paths.Binary]) != "foreign" {
		t.Fatal("foreign target changed")
	}
	if platform.ensureCalls != 0 || platform.publishCalls != 0 || platform.pathWrites != 0 {
		t.Fatalf("foreign target caused side effects: ensure=%d publish=%d path=%d", platform.ensureCalls, platform.publishCalls, platform.pathWrites)
	}
}

func TestFrontendInstallerUpgradeAndDowngrade(t *testing.T) {
	platform, paths, source, binding := frontendFixture(t)
	old := []byte("frontend-v0")
	platform.files[paths.Binary] = old
	sum := sha256.Sum256(old)
	raw, err := BuildFrontendOwnership(ReleaseBinding{
		ReleaseTag: "v1.2.2", SourceRevision: strings.Repeat("b", 40),
	}, paths, hex.EncodeToString(sum[:]), int64(len(old)))
	if err != nil {
		t.Fatal(err)
	}
	platform.files[paths.Ownership] = raw
	result, err := (FrontendInstaller{Platform: platform}).Install(t.Context(), source, paths, binding)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != FrontendUpgraded || string(platform.files[paths.Binary]) != "frontend-v1" {
		t.Fatalf("result=%+v binary=%q", result, platform.files[paths.Binary])
	}

	newer := append([]byte(nil), platform.files[paths.Binary]...)
	newerOwnership := append([]byte(nil), platform.files[paths.Ownership]...)
	platform.files[source] = []byte("frontend-old-bootstrap")
	olderBinding := ReleaseBinding{ReleaseTag: "v1.2.2", SourceRevision: strings.Repeat("c", 40)}
	_, err = (FrontendInstaller{Platform: platform}).Install(t.Context(), source, paths, olderBinding)
	if err == nil || !strings.Contains(err.Error(), "downgrade") {
		t.Fatalf("err=%v", err)
	}
	if string(platform.files[paths.Binary]) != string(newer) ||
		string(platform.files[paths.Ownership]) != string(newerOwnership) {
		t.Fatal("downgrade attempt changed verified frontend state")
	}
}

func TestFrontendInstallerRepairsInterruptedUpgradeOwnership(t *testing.T) {
	platform, paths, source, binding := frontendFixture(t)
	old := []byte("frontend-v0")
	sum := sha256.Sum256(old)
	ownership, err := BuildFrontendOwnership(ReleaseBinding{
		ReleaseTag: "v1.2.2", SourceRevision: strings.Repeat("b", 40),
	}, paths, hex.EncodeToString(sum[:]), int64(len(old)))
	if err != nil {
		t.Fatal(err)
	}
	platform.files[paths.Ownership] = ownership
	platform.files[paths.Binary] = append([]byte(nil), platform.files[source]...)

	result, err := (FrontendInstaller{Platform: platform}).Install(t.Context(), source, paths, binding)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != FrontendRepaired || platform.publishCalls != 0 {
		t.Fatalf("result=%+v publishCalls=%d", result, platform.publishCalls)
	}
	repaired, err := ParseFrontendOwnership(platform.files[paths.Ownership], paths)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.ReleaseTag != binding.ReleaseTag || repaired.SourceRevision != binding.SourceRevision {
		t.Fatalf("ownership=%+v", repaired)
	}
}

func TestFrontendInstallerRetriesLockedReplacement(t *testing.T) {
	platform, paths, source, binding := frontendFixture(t)
	old := []byte("frontend-v0")
	platform.files[paths.Binary] = old
	sum := sha256.Sum256(old)
	raw, err := BuildFrontendOwnership(ReleaseBinding{
		ReleaseTag: "v1.2.2", SourceRevision: strings.Repeat("b", 40),
	}, paths, hex.EncodeToString(sum[:]), int64(len(old)))
	if err != nil {
		t.Fatal(err)
	}
	platform.files[paths.Ownership] = raw
	platform.publishFailures = 2
	result, err := (FrontendInstaller{Platform: platform, Attempts: 3, RetryDelay: time.Millisecond}).Install(
		t.Context(), source, paths, binding,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != FrontendUpgraded || platform.publishCalls != 3 || platform.sleeps != 2 {
		t.Fatalf("result=%+v calls=%d sleeps=%d", result, platform.publishCalls, platform.sleeps)
	}

	platform.files[paths.Binary] = old
	platform.files[paths.Ownership] = raw
	platform.publishFailures = 3
	platform.publishCalls = 0
	_, err = (FrontendInstaller{Platform: platform, Attempts: 3, RetryDelay: time.Millisecond}).Install(
		t.Context(), source, paths, binding,
	)
	if err == nil || !strings.Contains(err.Error(), "after 3 attempts") || string(platform.files[paths.Binary]) != string(old) {
		t.Fatalf("err=%v binary=%q", err, platform.files[paths.Binary])
	}
}

func TestReconcileUserPathPreservesUnrelatedEntries(t *testing.T) {
	managed := "C:\\Users\\alice\\AppData\\Local\\Programs\\Loki\\bin"
	current := "C:\\One;" + strings.ToUpper(managed) + ";C:\\Two;" + managed
	next, changed, err := ReconcileUserPath(current, managed)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || next != "C:\\One;"+managed+";C:\\Two" {
		t.Fatalf("next=%q changed=%v", next, changed)
	}
	again, changed, err := ReconcileUserPath(next, managed)
	if err != nil || changed || again != next {
		t.Fatalf("again=%q changed=%v err=%v", again, changed, err)
	}
	trailing, changed, err := ReconcileUserPath("C:\\One;", managed)
	if err != nil || !changed || trailing != "C:\\One;"+managed {
		t.Fatalf("trailing=%q changed=%v err=%v", trailing, changed, err)
	}
}

func TestResolveFrontendPathsRejectsUNC(t *testing.T) {
	if _, err := ResolveFrontendPaths(`\\\\server\\share\\Local`); err == nil {
		t.Fatal("UNC LOCALAPPDATA accepted for Windows frontend")
	}
}
