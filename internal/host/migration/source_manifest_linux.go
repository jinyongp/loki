//go:build linux

package migration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"

	"loki/internal/admin"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/host/lifecycle"
	"loki/internal/platform/privateio"
	secret "loki/modules/secrets"
)

const sourceManifestLimit = 128 << 20

// SourceTransferManifest is public metadata sent by the existing host owner to
// its private helper. Fixed source/socket paths refer to that helper's verified
// mounts. Source keys, values, passphrases and task contexts have no fields here.
type SourceTransferManifest struct {
	Version         int                                  `json:"version"`
	Operation       string                               `json:"operation"`
	Policy          string                               `json:"policy"`
	SourceRoot      string                               `json:"source_root"`
	TargetSocket    string                               `json:"target_socket"`
	TargetUID       uint32                               `json:"target_uid"`
	Plan            appvalues.MigrationPlan              `json:"plan"`
	BaseEnvironment map[string]string                    `json:"base_environment"`
	Writes          []lifecycle.MigrationWriteCheckpoint `json:"writes"`
}

func (m SourceTransferManifest) valid() bool {
	if m.Version != 1 || !controlpolicy.ApplicationUUID.MatchString(m.Operation) || !validDigest(m.Policy) || m.TargetUID == 0 || m.Plan.Version != 1 || m.Plan.Revision == 0 || m.Plan.Mappings == nil || m.Writes == nil || len(m.Plan.Resources) == 0 || len(m.TargetSocket) >= 108 {
		return false
	}
	for _, path := range []string{m.SourceRoot, m.TargetSocket} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || len(path) > 4096 || strings.ContainsAny(path, "\x00\r\n") {
			return false
		}
	}
	return true
}

func WriteSourceTransferManifest(channel io.Writer, m SourceTransferManifest) error {
	if !m.valid() {
		return ErrSourceSession
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		return ErrSourceSession
	}
	defer clear(encoded)
	return privateio.WriteBytes(channel, encoded, sourceManifestLimit)
}

// ServeSourceTransferManifest accepts only the host's canonical typed metadata
// encoding before any source access. It uses a separate larger metadata frame
// because complete legacy mappings may exceed the ordinary 128KiB controls.
// Subsequent checkpoint events and ACKs retain their normal control bound.
func ServeSourceTransferManifest(ctx context.Context, channel io.ReadWriteCloser) error {
	if channel == nil {
		return ErrSourceSession
	}
	stop := context.AfterFunc(ctx, func() { channel.Close() })
	defer stop()
	encoded, err := privateio.ReadBytes(channel, sourceManifestLimit)
	if err != nil {
		channel.Close()
		return ErrSourceSession
	}
	defer clear(encoded)
	var manifest SourceTransferManifest
	if json.Unmarshal(encoded, &manifest) != nil || !manifest.valid() {
		channel.Close()
		return ErrSourceSession
	}
	canonical, err := json.Marshal(manifest)
	if err != nil || !bytes.Equal(canonical, encoded) {
		clear(canonical)
		channel.Close()
		return ErrSourceSession
	}
	clear(canonical)
	session := SourceSession{Source: secret.Controller{StateDirectory: manifest.SourceRoot}, Target: admin.ApplicationClient{Socket: manifest.TargetSocket, ExpectedUID: manifest.TargetUID}, Operation: manifest.Operation, Policy: manifest.Policy, Plan: manifest.Plan, BaseEnvironment: manifest.BaseEnvironment, Writes: manifest.Writes}
	return ServeSourceTransfer(ctx, session, channel)
}
