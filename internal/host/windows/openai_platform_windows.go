//go:build windows

package windows

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	credTypeGeneric         = 1
	credPersistLocalMachine = 2
	openAIHelperTimeout     = 2 * time.Minute
)

type windowsCredential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

var (
	advapi32Cred    = windows.NewLazySystemDLL("advapi32.dll")
	procCredWriteW  = advapi32Cred.NewProc("CredWriteW")
	procCredReadW   = advapi32Cred.NewProc("CredReadW")
	procCredDeleteW = advapi32Cred.NewProc("CredDeleteW")
	procCredFree    = advapi32Cred.NewProc("CredFree")
)

type WindowsCredentialManager struct{}

func (WindowsCredentialManager) Put(ctx context.Context, target, secret string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if target == "" || strings.ContainsAny(target, "\r\n\x00") {
		return errors.New("Windows credential target is invalid")
	}
	if err := validateRuntimeSecret(secret); err != nil {
		return err
	}
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	commentPtr, err := windows.UTF16PtrFromString("Loki managed OpenAI tunnel runtime key")
	if err != nil {
		return err
	}
	blob := []byte(secret)
	defer clear(blob)
	credential := windowsCredential{
		Type:               credTypeGeneric,
		TargetName:         targetPtr,
		Comment:            commentPtr,
		CredentialBlobSize: uint32(len(blob)),
		CredentialBlob:     &blob[0],
		Persist:            credPersistLocalMachine,
	}
	ret, _, callErr := procCredWriteW.Call(uintptr(unsafe.Pointer(&credential)), 0)
	if ret == 0 {
		if callErr != nil && callErr != windows.ERROR_SUCCESS {
			return fmt.Errorf("CredWriteW: %w", callErr)
		}
		return errors.New("CredWriteW failed")
	}
	return nil
}

func (WindowsCredentialManager) Get(ctx context.Context, target string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if target == "" || strings.ContainsAny(target, "\r\n\x00") {
		return "", errors.New("Windows credential target is invalid")
	}
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return "", err
	}
	var credential *windowsCredential
	ret, _, callErr := procCredReadW.Call(
		uintptr(unsafe.Pointer(targetPtr)),
		uintptr(credTypeGeneric),
		0,
		uintptr(unsafe.Pointer(&credential)),
	)
	if ret == 0 {
		if callErr != nil && callErr != windows.ERROR_SUCCESS {
			return "", fmt.Errorf("CredReadW: %w", callErr)
		}
		return "", errors.New("CredReadW failed")
	}
	if credential == nil {
		return "", errors.New("Windows Credential Manager returned no credential")
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(credential)))
	if credential.CredentialBlob == nil || credential.CredentialBlobSize == 0 ||
		credential.CredentialBlobSize > 4096 {
		return "", errors.New("Windows Credential Manager returned an invalid credential")
	}
	raw := append([]byte(nil), unsafe.Slice(credential.CredentialBlob, credential.CredentialBlobSize)...)
	defer clear(raw)
	secret := string(raw)
	if err = validateRuntimeSecret(secret); err != nil {
		return "", err
	}
	return secret, nil
}

func (WindowsCredentialManager) Delete(ctx context.Context, target string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if target == "" || strings.ContainsAny(target, "\r\n\x00") {
		return errors.New("Windows credential target is invalid")
	}
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	ret, _, callErr := procCredDeleteW.Call(
		uintptr(unsafe.Pointer(targetPtr)),
		uintptr(credTypeGeneric),
		0,
	)
	if ret == 0 {
		if errors.Is(callErr, windows.ERROR_NOT_FOUND) {
			return nil
		}
		if callErr != nil && callErr != windows.ERROR_SUCCESS {
			return fmt.Errorf("CredDeleteW: %w", callErr)
		}
		return errors.New("CredDeleteW failed")
	}
	return nil
}

type ExecOpenAIHelperRunner struct{}

func (ExecOpenAIHelperRunner) Run(
	ctx context.Context,
	executable string,
	arguments []string,
	overrides map[string]string,
) (NativeProbe, error) {
	if !filepath.IsAbs(executable) {
		return NativeProbe{}, errors.New("OpenAI helper executable path is not absolute")
	}
	info, err := OSStateFilesystem{}.Lstat(executable)
	if err != nil {
		return NativeProbe{}, err
	}
	if !info.Exists || !info.Regular || info.Reparse {
		return NativeProbe{}, errors.New("OpenAI helper executable is not a regular non-reparse file")
	}
	runCtx := ctx
	cancel := func() {}
	if _, ok := ctx.Deadline(); !ok {
		runCtx, cancel = context.WithTimeout(ctx, openAIHelperTimeout)
	}
	defer cancel()

	command := exec.CommandContext(runCtx, executable, arguments...)
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	command.Env = withoutEnvironment(os.Environ(), names...)
	for _, name := range names {
		value := overrides[name]
		if strings.ContainsRune(name, '=') || strings.ContainsRune(name, 0) ||
			strings.ContainsRune(value, 0) {
			return NativeProbe{}, errors.New("OpenAI helper environment override is invalid")
		}
		command.Env = append(command.Env, name+"="+value)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	probe := NativeProbe{
		ExitCode: 0,
		Stdout:   strings.TrimSpace(strings.ReplaceAll(stdout.String(), "\x00", "")),
		Stderr:   strings.TrimSpace(strings.ReplaceAll(stderr.String(), "\x00", "")),
	}
	if err == nil {
		return probe, nil
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return NativeProbe{}, errors.New("OpenAI helper process exceeded the execution timeout")
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		probe.ExitCode = normalizeNativeExitCode(exitError.ExitCode())
		return probe, nil
	}
	return NativeProbe{}, fmt.Errorf("start OpenAI helper: %w", err)
}

type WindowsOpenAILocalConnectionSource struct {
	LocalAppData string
	SystemRoot   string
}

func (source WindowsOpenAILocalConnectionSource) Read(ctx context.Context, distribution string) (ConnectionMaterial, error) {
	if err := ctx.Err(); err != nil {
		return ConnectionMaterial{}, err
	}
	localAppData := strings.TrimSpace(source.LocalAppData)
	if localAppData == "" {
		localAppData = strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	}
	systemRoot := strings.TrimSpace(source.SystemRoot)
	if systemRoot == "" {
		systemRoot = strings.TrimSpace(os.Getenv("SystemRoot"))
	}
	expected, err := ExpectedFromOptions(InstallOptions{Distribution: distribution}, localAppData, systemRoot)
	if err != nil {
		return ConnectionMaterial{}, err
	}
	filesystem := OSStateFilesystem{}
	state, err := InspectWindowsState(filesystem, expected)
	if err != nil {
		return ConnectionMaterial{}, err
	}
	if !state.Present || !state.Owned {
		return ConnectionMaterial{}, errors.New("OpenAI adapter refuses unverified Windows Loki connection state")
	}
	connectionPath := joinWindowsPath(expected.StateDir, "connection.json")
	tokenPath := joinWindowsPath(expected.StateDir, "mcp-token")
	for _, path := range []string{connectionPath, tokenPath} {
		info, statErr := filesystem.Lstat(path)
		if statErr != nil {
			return ConnectionMaterial{}, statErr
		}
		if !info.Exists || !info.Regular || info.Reparse {
			return ConnectionMaterial{}, errors.New("OpenAI adapter connection input is not a regular non-reparse file")
		}
	}
	connectionRaw, err := os.ReadFile(connectionPath)
	if err != nil {
		return ConnectionMaterial{}, err
	}
	tokenRaw, err := os.ReadFile(tokenPath)
	if err != nil {
		return ConnectionMaterial{}, err
	}
	snapshot, err := BuildReplicaSnapshot(expected, state, connectionRaw, tokenRaw)
	if err != nil {
		return ConnectionMaterial{}, err
	}
	if err = ctx.Err(); err != nil {
		return ConnectionMaterial{}, err
	}
	return ConnectionMaterial{
		LocalOrigin:        snapshot.LocalOrigin,
		Transport:          "streamable-http",
		Reachability:       "loopback",
		AuthenticationType: "bearer-token-file",
		Token:              string(tokenRaw),
	}, nil
}

type WindowsOpenAIProviderStore struct {
	Platform WindowsFrontendPlatform
}

func NewWindowsOpenAIProviderStore() WindowsOpenAIProviderStore {
	return WindowsOpenAIProviderStore{Platform: NewWindowsFrontendPlatform()}
}

func openAIPaths(root string) OpenAIProviderPaths {
	return OpenAIProviderPaths{
		Root:       root,
		Metadata:   joinWindowsPath(root, openAIAdapterMetadataFileName),
		StateDir:   joinWindowsPath(root, openAITunnelStateDirectory),
		ProfileDir: joinWindowsPath(root, openAITunnelProfileDirectory),
	}
}

func (store WindowsOpenAIProviderStore) Ensure(ctx context.Context, root string) (OpenAIProviderPaths, error) {
	paths := openAIPaths(root)
	for _, directory := range []string{paths.Root, paths.StateDir, paths.ProfileDir} {
		if err := store.Platform.EnsurePrivateDirectory(ctx, directory); err != nil {
			return OpenAIProviderPaths{}, err
		}
		if err := verifyPrivateACL(directory, true); err != nil {
			return OpenAIProviderPaths{}, err
		}
	}
	return paths, nil
}

func (store WindowsOpenAIProviderStore) Read(root string) (OpenAIProviderMetadata, bool, error) {
	paths := openAIPaths(root)
	info, err := OSStateFilesystem{}.Lstat(paths.Metadata)
	if err != nil {
		return OpenAIProviderMetadata{}, false, err
	}
	if !info.Exists {
		return OpenAIProviderMetadata{}, false, nil
	}
	if !info.Regular || info.Reparse {
		return OpenAIProviderMetadata{}, false, errors.New("OpenAI adapter metadata is not a regular non-reparse file")
	}
	if err = verifyPrivateACL(paths.Metadata, false); err != nil {
		return OpenAIProviderMetadata{}, false, err
	}
	raw, err := os.ReadFile(paths.Metadata)
	if err != nil {
		return OpenAIProviderMetadata{}, false, err
	}
	if len(raw) == 0 || len(raw) > 64*1024 {
		return OpenAIProviderMetadata{}, false, errors.New("OpenAI adapter metadata size is invalid")
	}
	metadata, err := parseOpenAIMetadata(raw)
	if err != nil {
		return OpenAIProviderMetadata{}, false, err
	}
	return metadata, true, nil
}

func (store WindowsOpenAIProviderStore) Write(ctx context.Context, root string, metadata OpenAIProviderMetadata) error {
	paths, err := store.Ensure(ctx, root)
	if err != nil {
		return err
	}
	raw, err := encodeOpenAIMetadata(metadata)
	if err != nil {
		return err
	}
	return store.Platform.WriteProtectedAtomic(ctx, paths.Metadata, raw)
}

func (store WindowsOpenAIProviderStore) Cleanup(ctx context.Context, root string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	paths := openAIPaths(root)
	rootInfo, err := OSStateFilesystem{}.Lstat(root)
	if err != nil {
		return err
	}
	if !rootInfo.Exists {
		return nil
	}
	if !rootInfo.Directory || rootInfo.Reparse {
		return errors.New("OpenAI provider root is not a real directory")
	}
	if err = verifyPrivateACL(root, true); err != nil {
		return err
	}
	for _, directory := range []string{paths.StateDir, paths.ProfileDir} {
		if err = removeOpenAIPrivateTree(directory); err != nil {
			return err
		}
	}
	metadataInfo, err := OSStateFilesystem{}.Lstat(paths.Metadata)
	if err != nil {
		return err
	}
	if metadataInfo.Exists {
		if !metadataInfo.Regular || metadataInfo.Reparse {
			return errors.New("OpenAI adapter metadata changed type before cleanup")
		}
		if err = verifyPrivateACL(paths.Metadata, false); err != nil {
			return err
		}
		if err = os.Remove(paths.Metadata); err != nil {
			return err
		}
	}
	return nil
}

func removeOpenAIPrivateTree(root string) error {
	info, err := OSStateFilesystem{}.Lstat(root)
	if err != nil {
		return err
	}
	if !info.Exists {
		return nil
	}
	if !info.Directory || info.Reparse {
		return errors.New("OpenAI native state path is not a real directory")
	}
	if err = verifyNoReparseTree(root); err != nil {
		return err
	}
	return os.RemoveAll(root)
}

func verifyNoReparseTree(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		info, statErr := OSStateFilesystem{}.Lstat(path)
		if statErr != nil {
			return statErr
		}
		if info.Reparse {
			return fmt.Errorf("OpenAI native state contains reparse path %q", path)
		}
		if info.Directory {
			if err = verifyNoReparseTree(path); err != nil {
				return err
			}
			continue
		}
		if !info.Regular {
			return fmt.Errorf("OpenAI native state contains unsupported path type %q", path)
		}
	}
	return nil
}

func NewWindowsOpenAIAdapter(setup OpenAISetupConfig) *OpenAIAdapter {
	return &OpenAIAdapter{
		Credentials: WindowsCredentialManager{},
		Runner:      ExecOpenAIHelperRunner{},
		Local:       WindowsOpenAILocalConnectionSource{},
		Store:       NewWindowsOpenAIProviderStore(),
		SetupConfig: setup,
	}
}
