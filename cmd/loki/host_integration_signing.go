package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"loki/internal/host/lifecycle"
)

const maxSigningKeyImportBytes = 64 << 10

type managedSigningMaterial struct {
	PrivateKey     []byte
	PublicKey      []byte
	PublicInfo     []byte
	GitConfig      []byte
	AllowedSigners []byte
	Info           lifecycle.ManagedSigningPublicInfo
}

func runHostSigningSetup(action string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("host integration "+action+" signing", flag.ContinueOnError)
	flags.SetOutput(stderr)
	system := flags.Bool("system", false, "operate on the system-wide host installation")
	stateRoot := flags.String("state-root", "", "host lifecycle state root")
	launcherLayout := flags.String("launcher-layout", "", "launcher service layout")
	jsonOutput := flags.Bool("json", false, "emit machine-readable JSON")
	interrupt := flags.Bool("interrupt-active-jobs", false, "explicitly approve interrupting active jobs")
	keyFile := flags.String("key-file", "", "private Ed25519 SSH signing key to import; omit to generate")
	keyStdin := flags.Bool("key-stdin", false, "read the private Ed25519 SSH signing key from stdin")
	identityName := flags.String("identity-name", "", "Git user.name for managed signing")
	identityEmail := flags.String("identity-email", "", "Git user.email for managed signing")
	if err := flags.Parse(args); err != nil || flags.NArg() != 1 || flags.Arg(0) != "signing" {
		fmt.Fprintf(stderr, "usage: loki host integration %s [OPTIONS] signing\n", action)
		return 2
	}
	options, err := resolveHostIntegrationOptions(hostIntegrationOptions{
		System: *system, StateRoot: strings.TrimSpace(*stateRoot),
		LauncherLayout: strings.TrimSpace(*launcherLayout), JSON: *jsonOutput, InterruptJobs: *interrupt,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if options.System && os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "system integration setup requires root")
		return 1
	}
	if *keyStdin && strings.TrimSpace(*keyFile) != "" {
		fmt.Fprintln(stderr, "--key-file and --key-stdin are mutually exclusive")
		return 2
	}
	if err = validateManagedSigningIdentity(*identityName, *identityEmail); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	store, err := lifecycle.OpenFileStore(options.StateRoot)
	if err != nil {
		fmt.Fprintln(stderr, "Loki is not installed for this scope.")
		return 1
	}
	if action == "rotate" {
		state, readErr := store.ReadManagedIntegrations(context.Background())
		if readErr != nil {
			fmt.Fprintln(stderr, readErr)
			return 1
		}
		if !state.Signing.Configured {
			fmt.Fprintln(stderr, "signing integration is not configured; run setup first")
			return 1
		}
	}
	var material managedSigningMaterial
	if *keyStdin {
		privateKey, readErr := io.ReadAll(io.LimitReader(hostIntegrationStdin, maxSigningKeyImportBytes+1))
		if readErr != nil || len(privateKey) == 0 || len(privateKey) > maxSigningKeyImportBytes {
			clear(privateKey)
			fmt.Fprintln(stderr, "signing key stdin is empty or exceeds the supported size")
			return 1
		}
		material, err = prepareManagedSigningMaterialBytes(context.Background(), privateKey, *identityName, *identityEmail)
		clear(privateKey)
	} else {
		material, err = prepareManagedSigningMaterial(context.Background(), strings.TrimSpace(*keyFile), *identityName, *identityEmail)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer clear(material.PrivateKey)

	backend, err := newHostComposeBackend(store)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	engine := &lifecycle.TransactionEngine{Store: store, Backend: backend, Now: lifecycleTimeNow}
	manager := lifecycle.Manager{Store: store, Jobs: backend, Maintainer: engine, Now: lifecycleTimeNow}
	err = manager.UpdateManagedComponentIntegration(context.Background(), "signing", true, func(ctx context.Context, store *lifecycle.FileStore) error {
		credentialDigest, writeErr := store.WriteManagedIntegrationFile(ctx, lifecycle.ManagedSigningCredentialFile, material.PrivateKey)
		if writeErr != nil {
			return writeErr
		}
		for _, item := range []struct {
			path string
			raw  []byte
		}{
			{lifecycle.ManagedSigningPublicKeyFile, material.PublicKey},
			{lifecycle.ManagedSigningPublicInfoFile, material.PublicInfo},
			{lifecycle.ManagedSigningGitConfigFile, material.GitConfig},
			{lifecycle.ManagedSigningAllowedSignersFile, material.AllowedSigners},
		} {
			if _, writeErr = store.WriteManagedIntegrationFile(ctx, item.path, item.raw); writeErr != nil {
				return writeErr
			}
		}
		state, readErr := store.ReadManagedIntegrations(ctx)
		if readErr != nil {
			return readErr
		}
		state.Signing = lifecycle.ManagedIntegrationToggle{
			Configured: true, Enabled: true,
			CredentialSHA256: credentialDigest,
			ConfigSHA256:     lifecycle.ManagedIntegrationDigest(material.PublicInfo),
		}
		return store.CommitManagedIntegrations(ctx, state, action+"-signing", lifecycleTimeNow())
	}, lifecycle.MutationOptions{InterruptActiveJobs: options.InterruptJobs})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOutput {
		if err = json.NewEncoder(stdout).Encode(material.Info); err != nil {
			fmt.Fprintln(stderr, "cannot encode signing setup result")
			return 1
		}
	} else {
		fmt.Fprintln(stdout, "Git signing is ready.")
		fmt.Fprintf(stdout, "  Public key: %s\n", material.Info.PublicKey)
		fmt.Fprintf(stdout, "  Fingerprint: %s\n", material.Info.Fingerprint)
		fmt.Fprintf(stdout, "  Identity: %s <%s>\n", material.Info.IdentityName, material.Info.IdentityEmail)
		fmt.Fprintln(stdout, "Register the public key as an SSH signing key with your Git provider.")
	}
	return 0
}

func removeManagedSigning(
	ctx context.Context,
	manager lifecycle.Manager,
	store *lifecycle.FileStore,
	options lifecycle.MutationOptions,
	now func() time.Time,
) error {
	return manager.UpdateManagedComponentIntegration(ctx, "signing", false, func(ctx context.Context, store *lifecycle.FileStore) error {
		for _, path := range []string{
			lifecycle.ManagedSigningCredentialFile,
			lifecycle.ManagedSigningPublicKeyFile,
			lifecycle.ManagedSigningPublicInfoFile,
			lifecycle.ManagedSigningGitConfigFile,
			lifecycle.ManagedSigningAllowedSignersFile,
		} {
			if err := store.RemoveManagedIntegrationFile(ctx, path); err != nil {
				return err
			}
		}
		state, err := store.ReadManagedIntegrations(ctx)
		if err != nil {
			return err
		}
		state.Signing = lifecycle.ManagedIntegrationToggle{}
		return store.CommitManagedIntegrations(ctx, state, "remove-signing", now())
	}, options)
}

func validateManagedSigningIdentity(name, email string) error {
	name = strings.TrimSpace(name)
	email = strings.TrimSpace(email)
	if name == "" || len(name) > 256 || strings.ContainsAny(name, "\r\n\x00") {
		return errors.New("--identity-name must be a non-empty single-line Git identity")
	}
	if email == "" || len(email) > 320 || strings.ContainsAny(email, " \t\r\n\x00") || !strings.Contains(email, "@") {
		return errors.New("--identity-email must be a non-empty email address without whitespace")
	}
	return nil
}

func prepareManagedSigningMaterial(
	ctx context.Context,
	sourceKey, identityName, identityEmail string,
) (managedSigningMaterial, error) {
	if sourceKey == "" {
		return prepareManagedSigningMaterialBytes(ctx, nil, identityName, identityEmail)
	}
	privateKey, err := readSigningImportFile(sourceKey)
	if err != nil {
		return managedSigningMaterial{}, err
	}
	return prepareManagedSigningMaterialBytes(ctx, privateKey, identityName, identityEmail)
}

func prepareManagedSigningMaterialBytes(
	ctx context.Context,
	imported []byte,
	identityName, identityEmail string,
) (managedSigningMaterial, error) {
	var zero managedSigningMaterial
	if err := validateManagedSigningIdentity(identityName, identityEmail); err != nil {
		return zero, err
	}
	var (
		privateKey  []byte
		public      string
		fingerprint string
		err         error
	)
	if len(imported) == 0 {
		privateKey, public, fingerprint, err = generateManagedEd25519Key()
		if err != nil {
			return zero, fmt.Errorf("generate managed signing key: %w", err)
		}
	} else {
		privateKey = append([]byte(nil), imported...)
		public, fingerprint, err = publicKeyFromPrivateBytes(ctx, privateKey)
		if err != nil {
			clear(privateKey)
			return zero, fmt.Errorf("validate managed signing key: %w", err)
		}
	}
	info := lifecycle.ManagedSigningPublicInfo{
		Version: 1, PublicKey: public, Fingerprint: fingerprint,
		IdentityName: strings.TrimSpace(identityName), IdentityEmail: strings.TrimSpace(identityEmail),
	}
	if !info.Valid() {
		return zero, errors.New("managed signing public information is invalid")
	}
	infoRaw, err := json.Marshal(info)
	if err != nil {
		return zero, err
	}
	infoRaw = append(infoRaw, '\n')
	publicLine := []byte(public + "\n")
	gitConfig := []byte(
		"[user]\n" +
			"\tname = " + strconv.Quote(info.IdentityName) + "\n" +
			"\temail = " + strconv.Quote(info.IdentityEmail) + "\n" +
			"\tsigningKey = /home/runner/.ssh/id_ed25519.pub\n\n" +
			"[gpg \"ssh\"]\n" +
			"\tallowedSignersFile = /etc/loki-go/allowed_signers\n\n" +
			"[commit]\n\tgpgSign = true\n\n" +
			"[tag]\n\tgpgSign = true\n\tforceSignAnnotated = true\n",
	)
	return managedSigningMaterial{
		PrivateKey: privateKey, PublicKey: publicLine, PublicInfo: infoRaw, GitConfig: gitConfig,
		AllowedSigners: []byte(info.IdentityEmail + " " + public + "\n"), Info: info,
	}, nil
}

func appendSSHUint32(dst []byte, value uint32) []byte {
	var raw [4]byte
	binary.BigEndian.PutUint32(raw[:], value)
	return append(dst, raw[:]...)
}

func appendSSHString(dst, value []byte) []byte {
	dst = appendSSHUint32(dst, uint32(len(value)))
	return append(dst, value...)
}

func managedEd25519PublicBlob(public ed25519.PublicKey) []byte {
	var blob []byte
	blob = appendSSHString(blob, []byte("ssh-ed25519"))
	blob = appendSSHString(blob, public)
	return blob
}

func publicLineAndFingerprint(blob []byte) (string, string, error) {
	first, rest, ok := consumeSSHString(blob)
	if !ok || string(first) != "ssh-ed25519" {
		return "", "", errors.New("managed signing public key is not Ed25519")
	}
	public, rest, ok := consumeSSHString(rest)
	if !ok || len(public) != ed25519.PublicKeySize || len(rest) != 0 {
		return "", "", errors.New("managed signing public key payload is invalid")
	}
	sum := sha256.Sum256(blob)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob),
		"SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), nil
}

func consumeSSHString(raw []byte) ([]byte, []byte, bool) {
	if len(raw) < 4 {
		return nil, nil, false
	}
	size := int(binary.BigEndian.Uint32(raw[:4]))
	if size < 0 || size > len(raw)-4 {
		return nil, nil, false
	}
	return raw[4 : 4+size], raw[4+size:], true
}

func generateManagedEd25519Key() ([]byte, string, string, error) {
	public, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", "", err
	}
	publicBlob := managedEd25519PublicBlob(public)
	publicLine, fingerprint, err := publicLineAndFingerprint(publicBlob)
	if err != nil {
		return nil, "", "", err
	}
	var checkRaw [4]byte
	if _, err = rand.Read(checkRaw[:]); err != nil {
		return nil, "", "", err
	}
	check := binary.BigEndian.Uint32(checkRaw[:])
	var privateBlock []byte
	privateBlock = appendSSHUint32(privateBlock, check)
	privateBlock = appendSSHUint32(privateBlock, check)
	privateBlock = appendSSHString(privateBlock, []byte("ssh-ed25519"))
	privateBlock = appendSSHString(privateBlock, public)
	privateBlock = appendSSHString(privateBlock, privateKey)
	privateBlock = appendSSHString(privateBlock, []byte("loki managed signing"))
	for padding := byte(1); len(privateBlock)%8 != 0; padding++ {
		privateBlock = append(privateBlock, padding)
	}
	payload := []byte("openssh-key-v1\x00")
	payload = appendSSHString(payload, []byte("none"))
	payload = appendSSHString(payload, []byte("none"))
	payload = appendSSHString(payload, nil)
	payload = appendSSHUint32(payload, 1)
	payload = appendSSHString(payload, publicBlob)
	payload = appendSSHString(payload, privateBlock)
	encoded := pem.EncodeToMemory(&pem.Block{Type: "OPENSSH PRIVATE KEY", Bytes: payload})
	if len(encoded) == 0 {
		return nil, "", "", errors.New("cannot encode managed signing key")
	}
	return encoded, publicLine, fingerprint, nil
}

func publicKeyFromPrivateBytes(ctx context.Context, privateKey []byte) (string, string, error) {
	fd, err := unix.MemfdCreate("loki-signing-import", unix.MFD_CLOEXEC)
	if err != nil {
		return "", "", err
	}
	file := os.NewFile(uintptr(fd), "loki-signing-import")
	defer file.Close()
	if err = unix.Fchmod(fd, 0600); err != nil {
		return "", "", err
	}
	if _, err = file.Write(privateKey); err != nil {
		return "", "", err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/ssh-keygen", "-y", "-P", "", "-f", "/proc/self/fd/3")
	cmd.ExtraFiles = []*os.File{file}
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", "", errors.New("managed signing key must be an unencrypted Ed25519 OpenSSH private key")
	}
	fields := strings.Fields(string(output))
	if len(fields) < 2 || fields[0] != "ssh-ed25519" {
		return "", "", errors.New("managed signing key must be an unencrypted Ed25519 OpenSSH private key")
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return "", "", errors.New("managed signing public key is invalid")
	}
	return publicLineAndFingerprint(blob)
}

func readSigningImportFile(path string) ([]byte, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) || path == string(filepath.Separator) || strings.ContainsRune(path, 0) {
		return nil, errors.New("--key-file must be a clean absolute path")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "signing-key-import")
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, errors.New("signing key import must be a private 0600 regular file")
	}
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil || int(stat.Uid) != os.Geteuid() {
		return nil, errors.New("signing key import owner is invalid")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxSigningKeyImportBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > maxSigningKeyImportBytes {
		return nil, errors.New("signing key import exceeds the supported size")
	}
	return raw, nil
}
