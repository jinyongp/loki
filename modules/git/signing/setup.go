package signing

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loki/internal/daemon"
	"loki/internal/process"
	signingpublic "loki/modules/git/signing/public"
)

type PublicIdentity = signingpublic.PublicIdentity

func ValidateIdentity(name, email string) error { return signingpublic.ValidateIdentity(name, email) }

func saveSigningFile(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("signing file is not an owned regular file")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".signing-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Setup executes only the Git module's declared OpenSSH program. The private
// key stays inside the signing owner's root-only volume. Repeated setup keeps
// its existing key unless an explicit import supplies a replacement.
func Setup(ctx context.Context, state, keygen, name, email string, imported []byte) (PublicIdentity, error) {
	var result PublicIdentity
	if ValidateIdentity(name, email) != nil || !filepath.IsAbs(keygen) || len(imported) > 64<<10 {
		return result, fmt.Errorf("invalid Git signing setup input")
	}
	if err := daemon.PrivateDirectory(state); err != nil {
		return result, err
	}
	directory, err := os.MkdirTemp(state, ".setup-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(directory)
	keyPath := filepath.Join(directory, "key")
	if imported == nil {
		info, statErr := os.Lstat(filepath.Join(state, "key"))
		if statErr == nil {
			if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 64<<10 {
				return result, fmt.Errorf("existing signing key has unsafe storage")
			}
			imported, err = os.ReadFile(filepath.Join(state, "key"))
			if err != nil {
				return result, err
			}
			defer clear(imported)
		} else if !os.IsNotExist(statErr) {
			return result, statErr
		}
	}
	run := func(arguments []string) (process.Result, error) {
		result, err := (process.ContainerSupervisor{}).Run(ctx, process.Spec{Argv: append([]string{keygen}, arguments...), CWD: directory, Env: []string{"HOME=" + directory, "PATH=/usr/bin:/bin", "LANG=C.UTF-8"}, Timeout: 15 * time.Second, MaxOutput: 4096})
		if err != nil || result.ExitCode != 0 || result.TimedOut || result.Canceled || result.Truncated {
			return result, fmt.Errorf("Git module's signing key tool could not complete")
		}
		return result, nil
	}
	if imported != nil {
		if len(imported) == 0 {
			return result, fmt.Errorf("imported signing key is empty")
		}
		if err := saveSigningFile(keyPath, imported); err != nil {
			return result, err
		}
	} else {
		if _, err := run([]string{"-q", "-t", "ed25519", "-N", "", "-C", "", "-f", keyPath}); err != nil {
			return result, err
		}
	}
	derived, err := run([]string{"-y", "-P", "", "-f", keyPath})
	if err != nil {
		return result, err
	}
	fields := strings.Fields(derived.Output)
	if len(fields) < 2 || fields[0] != "ssh-ed25519" {
		return result, fmt.Errorf("Git signing requires an unencrypted Ed25519 SSH private key")
	}
	public := fields[0] + " " + fields[1]
	wire, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return result, fmt.Errorf("invalid generated public key")
	}
	digest := sha256.Sum256(wire)
	result = PublicIdentity{Schema: 1, Name: name, Email: email, PublicKey: public, Fingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(digest[:]), Keygen: keygen}
	if err := result.Validate(); err != nil {
		return PublicIdentity{}, err
	}
	private, err := os.ReadFile(keyPath)
	if err != nil {
		return PublicIdentity{}, err
	}
	defer clear(private)
	if err := saveSigningFile(filepath.Join(state, "key"), private); err != nil {
		return PublicIdentity{}, err
	}
	metadata, err := json.Marshal(result)
	if err != nil {
		return PublicIdentity{}, err
	}
	if err := saveSigningFile(filepath.Join(state, "public.json"), metadata); err != nil {
		return PublicIdentity{}, err
	}
	return result, nil
}
