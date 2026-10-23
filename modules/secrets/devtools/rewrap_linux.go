//go:build linux

package devtools

import (
	"context"
	"os"

	"golang.org/x/sys/unix"
)

// Rewrap authenticates protection through two private inputs. The anonymous
// sealed file is inherited only by this CLI child and has no filesystem name.
// A nil current credential resumes a published operation, never bypasses auth.
func (p *Provider) Rewrap(ctx context.Context, request string, current, next []byte) (ProtectionResult, error) {
	if !uuid.MatchString(request) || len(current) > 65536 || len(next) == 0 || len(next) > 65536 {
		return ProtectionResult{}, ErrUnavailable
	}
	fd, err := unix.MemfdCreate("loki-protection", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return ProtectionResult{}, ErrUnavailable
	}
	f := os.NewFile(uintptr(fd), "private-protection")
	defer f.Close()
	if err := f.Truncate(int64(len(next))); err != nil {
		return ProtectionResult{}, ErrUnavailable
	}
	mapping, err := unix.Mmap(fd, 0, len(next), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return ProtectionResult{}, ErrUnavailable
	}
	copy(mapping, next)
	defer func() {
		// A cold CLI can pass FD3 to its new session daemon. Clearing the
		// existing shared mapping erases those references before any result
		// returns, including errors/cancellation. Future writers are sealed out.
		clear(mapping)
		_ = unix.Munmap(mapping)
	}()
	if _, err := unix.FcntlInt(f.Fd(), unix.F_ADD_SEALS, unix.F_SEAL_SEAL|unix.F_SEAL_SHRINK|unix.F_SEAL_GROW|unix.F_SEAL_FUTURE_WRITE); err != nil {
		return ProtectionResult{}, ErrUnavailable
	}
	args := []string{"vault", "rewrap", "--from-backend", "passphrase", "--backend", "passphrase", "--request-id", request, "--new-passphrase-file", "/proc/self/fd/3"}
	if len(current) != 0 {
		args = append(args, "--current-passphrase-stdin")
	}
	var result ProtectionResult
	if err := p.invokeFiles(ctx, args, current, &result, []*os.File{f}); err != nil {
		return ProtectionResult{}, err
	}
	if result.RequestID != request || !validProtectionStatus(result.Item) {
		return ProtectionResult{}, ErrUnavailable
	}
	return result, nil
}
