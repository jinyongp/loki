//go:build linux

package application

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/platform/privateio"
)

// CommitRestoredCandidate publishes only a fresh host-owned candidate whose
// broker sealed every app-only logical import for the original operation.
// The existing host journal owns admission, per-profile intents and release.
// Current value/task roots are never copied over the serving generation.
func CommitRestoredCandidate(options PrepareOptions, expected Identity, generation string) (Identity, error) {
	if os.Geteuid() != 0 || !controlpolicy.ApplicationUUID.MatchString(options.MigrationOperation) || !generationID.MatchString(generation) || generation == expected.ActiveGeneration {
		return Identity{}, invalid
	}
	path := filepath.Join(options.StateRoot, "generations", generation, "metadata", "restore-seal.json")
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return Identity{}, invalid
	}
	file := os.NewFile(uintptr(fd), "original-restore-seal")
	defer file.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Uid != expected.UID || st.Nlink != 1 || st.Size < 1 || st.Size > 1<<20 {
		return Identity{}, invalid
	}
	data := make([]byte, int(st.Size))
	if _, err := file.ReadAt(data, 0); err != nil {
		return Identity{}, invalid
	}
	var seal appvalues.ApplicationRestoreSeal
	if privateio.Decode(data, &seal) != nil || seal.Version != 1 || seal.Operation != options.MigrationOperation || seal.Deployment != expected.DeploymentID || seal.Imports == nil {
		return Identity{}, invalid
	}
	policy, err := PolicyDigest(options)
	if err != nil || seal.Policy != policy {
		return Identity{}, invalid
	}
	resources, err := controlpolicy.CanonicalApplications(options.Resources, options.Contract.Environment)
	if err != nil || len(seal.Imports) != len(resources) {
		return Identity{}, invalid
	}
	allowed := map[string]bool{}
	for _, r := range resources {
		allowed[r.ID] = true
	}
	for _, p := range seal.Imports {
		if !allowed[p.Resource] || p.Version != 1 || p.Operation != seal.Operation || p.Policy != seal.Policy || !controlpolicy.ApplicationUUID.MatchString(p.Request) || !policyDigest.MatchString(p.Digest) || !policyDigest.MatchString(p.CipherSHA256) || strings.ContainsAny(p.Resource, "/\\") {
			return Identity{}, invalid
		}
		delete(allowed, p.Resource)
	}
	return CommitCandidate(options, expected, generation)
}
