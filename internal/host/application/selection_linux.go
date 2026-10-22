//go:build linux

package application

import (
	"encoding/json"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
	"loki/internal/platform/safeio"
)

// CommitCandidate publishes the generation and expected policy together. The
// canonical host migration operation must first checkpoint successful private
// comparison, and retain its maintenance fence through service restart/health.
// This file owns only selector metadata; it never opens provider data or keys.
func CommitCandidate(options PrepareOptions, expected Identity, generation string) (Identity, error) {
	if os.Geteuid() != 0 || !filepath.IsAbs(options.IdentityFile) || filepath.Clean(options.IdentityFile) != options.IdentityFile || !generationID.MatchString(generation) || generation == expected.ActiveGeneration {
		return Identity{}, invalid
	}
	// A subordinate metadata mutex prevents a lost comparison-and-publication
	// race. Admission/recovery authority remains the existing host journal.
	parent, err := unix.Openat2(unix.AT_FDCWD, filepath.Dir(options.IdentityFile), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return Identity{}, invalid
	}
	defer unix.Close(parent)
	var st unix.Stat_t
	if unix.Fstat(parent, &st) != nil || st.Uid != 0 || st.Mode&0022 != 0 {
		return Identity{}, invalid
	}
	fd, err := unix.Openat(parent, filepath.Base(options.IdentityFile)+".lock", unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return Identity{}, invalid
	}
	defer unix.Close(fd)
	if unix.Fstat(fd, &st) != nil || st.Uid != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Nlink != 1 || st.Size != 0 || unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) != nil {
		return Identity{}, invalid
	}
	if _, err := os.Lstat(options.IdentityFile); err != nil {
		return Identity{}, invalid
	}
	current, err := loadIdentity(options)
	if err != nil {
		return Identity{}, err
	}
	if current.Version == 2 && current.ActiveGeneration == generation {
		// A lost selector acknowledgement reuses the installed identity. Check
		// the actual config/catalog digest again rather than rebinding on boot.
		activeOptions := options
		activeOptions.MigrationOperation = ""
		active, err := PrepareState(activeOptions)
		want := expected
		want.Version, want.ActiveGeneration, want.PolicySHA256 = 2, generation, active.Layout.PolicySHA256
		if err != nil || active.Identity != want {
			return Identity{}, invalid
		}
		return active.Identity, nil
	}
	if current != expected {
		return Identity{}, invalid
	}
	prepared, err := PrepareCandidate(options, expected, generation)
	if err != nil {
		return Identity{}, err
	}
	raw, err := json.Marshal(prepared.Identity)
	if err != nil {
		return Identity{}, err
	}
	if err := safeio.PublishPrivate(options.IdentityFile, raw, true); err != nil {
		return Identity{}, err
	}
	return prepared.Identity, nil
}
