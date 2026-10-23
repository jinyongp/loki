//go:build linux

package application

import (
	"encoding/json"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
	"loki/internal/platform/safeio"
)

// CommitPolicy changes only the policy of the exact retained generation. The
// host caller must hold its original transaction and stopped launcher journal.
// Ordinary startup never calls this compare-and-swap operation.
func CommitPolicy(options PrepareOptions, expected Identity) (Identity, error) {
	if os.Geteuid() != 0 || expected.Version != 2 || !policyDigest.MatchString(expected.PolicySHA256) || !filepath.IsAbs(options.IdentityFile) || filepath.Clean(options.IdentityFile) != options.IdentityFile {
		return Identity{}, invalid
	}
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
	current, err := ReadIdentity(options.IdentityFile)
	if err != nil || current != expected || current.UID != options.UID || current.GID != options.GID || current.AgentUID != options.AgentUID || current.MetadataGID != options.MetadataGID || RequireExistingGeneration(options.StateRoot, current) != nil {
		return Identity{}, invalid
	}
	policy, err := PolicyDigest(options)
	if err != nil {
		return Identity{}, err
	}
	current.PolicySHA256 = policy
	body, err := json.Marshal(current)
	if err != nil {
		return Identity{}, err
	}
	if err := safeio.PublishPrivate(options.IdentityFile, body, true); err != nil {
		return Identity{}, err
	}
	return current, nil
}
