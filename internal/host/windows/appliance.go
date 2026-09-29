package windows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loki/internal/progress"
)

type ReleaseAppliancePreparer struct {
	Binding  ReleaseBinding
	Client   *http.Client
	Progress progress.Reporter
}

func (preparer ReleaseAppliancePreparer) PrepareAppliance(ctx context.Context, options InstallOptions) (PreparedAppliance, func(), error) {
	if preparer.Binding.WSLAppliance.Length <= 0 || preparer.Binding.WSLAppliance.SHA256 == "" ||
		preparer.Binding.ReleaseTag == "" {
		return PreparedAppliance{}, nil, errors.New("Windows frontend release binding is incomplete")
	}
	root, err := os.MkdirTemp("", "loki-wsl-*")
	if err != nil {
		return PreparedAppliance{}, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	target := filepath.Join(root, "loki-wsl-amd64.wsl")
	if options.LocalAppliance != "" {
		if err = copyVerifiedApplianceProgress(options.LocalAppliance, target, preparer.Binding.WSLAppliance, preparer.Progress); err != nil {
			cleanup()
			return PreparedAppliance{}, nil, err
		}
	} else {
		client := preparer.Client
		if client == nil {
			client = &http.Client{Timeout: 30 * time.Minute}
		}
		url := "https://github.com/jinyongp/loki/releases/download/" + preparer.Binding.ReleaseTag + "/loki-wsl-amd64.wsl"
		if err = downloadVerifiedApplianceProgress(ctx, client, url, target, preparer.Binding.WSLAppliance, preparer.Progress); err != nil {
			cleanup()
			return PreparedAppliance{}, nil, err
		}
	}
	return PreparedAppliance{Path: target}, cleanup, nil
}

func copyVerifiedAppliance(source, target string, expected FileBinding) error {
	return copyVerifiedApplianceProgress(source, target, expected, nil)
}

func copyVerifiedApplianceProgress(source, target string, expected FileBinding, reporter progress.Reporter) error {
	info, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("inspect local WSL appliance: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != expected.Length {
		return errors.New("local WSL appliance does not match accepted release length")
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	openedInfo, err := input.Stat()
	if err != nil {
		return err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) || openedInfo.Size() != expected.Length {
		return errors.New("local WSL appliance changed while being opened")
	}
	reader := progress.NewReader(input, reporter, progress.ReaderOptions{
		Operation: "install", Phase: "copy-appliance", Label: "WSL appliance", TotalBytes: expected.Length,
	})
	stopHeartbeat := progress.StartHeartbeat(context.Background(), reporter, progress.HeartbeatOptions{
		Operation: "install", Phase: "copy-appliance", Message: "Still copying the WSL appliance",
	})
	err = writeVerifiedAppliance(reader, target, expected)
	stopHeartbeat()
	return err
}

func downloadVerifiedAppliance(ctx context.Context, client *http.Client, sourceURL, target string, expected FileBinding) error {
	return downloadVerifiedApplianceProgress(ctx, client, sourceURL, target, expected, nil)
}

func downloadVerifiedApplianceProgress(
	ctx context.Context,
	client *http.Client,
	sourceURL, target string,
	expected FileBinding,
	reporter progress.Reporter,
) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download WSL appliance returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength >= 0 && response.ContentLength != expected.Length {
		return errors.New("downloaded WSL appliance length does not match accepted release")
	}
	reader := progress.NewReader(
		io.LimitReader(response.Body, expected.Length+1),
		reporter,
		progress.ReaderOptions{
			Operation: "install", Phase: "download-appliance", Label: "WSL appliance", TotalBytes: expected.Length,
		},
	)
	stopHeartbeat := progress.StartHeartbeat(ctx, reporter, progress.HeartbeatOptions{
		Operation: "install", Phase: "download-appliance", Message: "Still downloading the WSL appliance",
	})
	err = writeVerifiedAppliance(reader, target, expected)
	stopHeartbeat()
	return err
}

func writeVerifiedAppliance(reader io.Reader, target string, expected FileBinding) error {
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	digest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, digest), reader)
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(target)
		return copyErr
	}
	if syncErr != nil {
		_ = os.Remove(target)
		return syncErr
	}
	if closeErr != nil {
		_ = os.Remove(target)
		return closeErr
	}
	if written != expected.Length || !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), expected.SHA256) {
		_ = os.Remove(target)
		return errors.New("WSL appliance bytes do not match accepted release")
	}
	return nil
}
