package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loki/internal/config"
	"loki/internal/daemon"
	"loki/internal/platform/netguard"
)

// RefreshKeys preserves the last usable key file on fetch or validation failure.
// The optional client is injected by tests; normal execution pins public DNS
// destinations, verifies TLS and accepts no cross-host redirects or ambient proxy.
func RefreshKeys(ctx context.Context, team, target string, gid int, client *http.Client) error {
	if !config.Hostname(team) || !strings.HasSuffix(team, ".cloudflareaccess.com") || !filepath.IsAbs(target) || gid < 0 {
		return errors.New("invalid JWKS refresh configuration")
	}
	if client == nil {
		transport := &http.Transport{DialContext: (netguard.Policy{}).Dial, DisableKeepAlives: true, ResponseHeaderTimeout: 15 * time.Second}
		defer transport.CloseIdleConnections()
		client = &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", "https://"+team+"/cdn-cgi/access/certs", nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "loki-jwks-refresh/1")
	response, err := client.Do(request)
	if err != nil {
		return errors.New("Cloudflare JWKS fetch failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return errors.New("Cloudflare JWKS returned an unsuccessful response")
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1048577))
	if err != nil || len(payload) > 1048576 {
		return errors.New("Cloudflare JWKS response exceeds limit or cannot be read")
	}
	var document struct{ Keys []json.RawMessage }
	if json.Unmarshal(payload, &document) != nil || len(document.Keys) == 0 {
		return errors.New("Cloudflare JWKS contains no keys")
	}
	parent := filepath.Dir(target)
	if err = daemon.PrivateDirectory(parent); err != nil {
		return err
	}
	if info, err := os.Lstat(target); err == nil && !info.Mode().IsRegular() {
		return errors.New("JWKS target must be a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(parent, ".cloudflare-jwks-")
	if err != nil {
		return err
	}
	defer file.Close()
	defer os.Remove(file.Name())
	if _, err = file.Write(payload); err != nil {
		return err
	}
	if err = file.Chmod(0640); err != nil {
		return err
	}
	if err = file.Chown(-1, gid); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), target); err != nil {
		return err
	}
	directory, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
