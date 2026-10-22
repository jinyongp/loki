package devtools

import (
	"context"
	"path/filepath"
	"strings"

	"filippo.io/age"
)

// ProfileTransfer is metadata for an application-only logical archive. The
// isolated provider owns decoding/re-encryption; Loki never reads its vault.
type ProfileTransfer struct {
	Profile       string
	SourceProfile string
	Digest        string
	TargetExists  bool
	Changed       bool
	Replayed      bool
}

type profileTransferResult struct {
	Item struct {
		Profile       string `json:"profile"`
		SourceProfile string `json:"source_profile"`
		Values        bool   `json:"values"`
		Tasks         bool   `json:"tasks"`
		Path          string `json:"path"`
	} `json:"item"`
	Digest       string `json:"digest"`
	TargetExists bool   `json:"target_exists"`
	Changed      bool   `json:"changed"`
	Replayed     bool   `json:"replayed"`
}

func transferPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/" && !strings.ContainsAny(path, "\x00\r\n")
}

// ExportApplication requires an explicit operator public recipient and output
// path. It never prompts, prepares a private identity, or exports a coordination
// profile. The caller owns the private destination and maintenance fence.
func (p *Provider) ExportApplication(ctx context.Context, app, recipient, output string) error {
	r, err := age.ParseX25519Recipient(recipient)
	if err != nil || r.String() != recipient || !profile.MatchString(app) || !transferPath(output) {
		return ErrUnavailable
	}
	var result profileTransferResult
	if err := p.invoke(ctx, []string{"profile", "export", "--profile", app, "--recipient", recipient, "--output", output}, nil, &result); err != nil {
		return err
	}
	if !result.Changed || result.Item.Profile != app || !result.Item.Values || result.Item.Tasks || result.Item.Path != output {
		return ErrUnavailable
	}
	return nil
}

func (p *Provider) previewApplication(ctx context.Context, app, archive, identity string) (profileTransferResult, error) {
	var result profileTransferResult
	if !profile.MatchString(app) || !transferPath(archive) || !transferPath(identity) {
		return result, ErrUnavailable
	}
	err := p.invoke(ctx, []string{"profile", "import", "--file", archive, "--identity-file", identity, "--as", app}, nil, &result)
	if err != nil {
		return profileTransferResult{}, err
	}
	if result.Item.Profile != app || !profile.MatchString(result.Item.SourceProfile) || !result.Item.Values || result.Item.Tasks || !editRevision.MatchString(result.Digest) || result.Changed || result.Replayed {
		return profileTransferResult{}, ErrUnavailable
	}
	return result, nil
}

func transferMetadata(result profileTransferResult) ProfileTransfer {
	return ProfileTransfer{Profile: result.Item.Profile, SourceProfile: result.Item.SourceProfile, Digest: result.Digest, TargetExists: result.TargetExists, Changed: result.Changed, Replayed: result.Replayed}
}

func (p *Provider) PreviewApplicationImport(ctx context.Context, app, archive, identity string) (ProfileTransfer, error) {
	result, err := p.previewApplication(ctx, app, archive, identity)
	if err != nil {
		return ProfileTransfer{}, err
	}
	return transferMetadata(result), nil
}

// ImportApplication validates application-only contents again before every
// apply, using the provider's exact preview digest to reject archive changes.
// Replacement is intentionally unavailable: restore loads an inactive empty
// generation, never overwrites a currently serving profile or task root.
func (p *Provider) ImportApplication(ctx context.Context, app, archive, identity, digest, request string) (ProfileTransfer, error) {
	if !editRevision.MatchString(digest) || !uuid.MatchString(request) {
		return ProfileTransfer{}, ErrUnavailable
	}
	preview, err := p.previewApplication(ctx, app, archive, identity)
	if err != nil {
		return ProfileTransfer{}, err
	}
	if !preview.TargetExists && preview.Digest != digest {
		return ProfileTransfer{}, ErrUnavailable
	}
	// A successful import changes the target-bound preview digest. For an
	// existing target only the original upstream receipt may authorize replay;
	// requiring today's preview digest would lose that receipt after ACK loss.
	// --replace remains absent, so a new request cannot overwrite the target.
	var result profileTransferResult
	err = p.invoke(ctx, []string{"profile", "import", "--file", archive, "--identity-file", identity, "--as", app, "--apply", digest, "--request-id", request}, nil, &result)
	if err != nil {
		return ProfileTransfer{}, err
	}
	if result.Item.Profile != app || result.Item.SourceProfile != preview.Item.SourceProfile || !result.Item.Values || result.Item.Tasks || result.Digest != digest || !result.Changed && !result.Replayed || preview.TargetExists && !result.Replayed {
		return ProfileTransfer{}, ErrUnavailable
	}
	return transferMetadata(result), nil
}
