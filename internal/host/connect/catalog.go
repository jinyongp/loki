package connect

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
)

const CatalogSchemaVersion = 1

const maxHelperAssetBytes = int64(1 << 30)

var (
	catalogTokenPattern   = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	catalogVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)
	catalogSHA256Pattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Asset struct {
	SourceURL    string `json:"source_url"`
	SourceSHA256 string `json:"source_sha256"`
	SourceLength int64  `json:"source_length"`
	MirrorAsset  string `json:"mirror_asset"`
}

type Helper struct {
	ID             string   `json:"id"`
	Provider       string   `json:"provider"`
	Version        string   `json:"version"`
	Platform       string   `json:"platform"`
	Executable     string   `json:"executable"`
	ArchiveMembers []string `json:"archive_members"`
	Archive        Asset    `json:"archive"`
	LicenseReport  Asset    `json:"license_report"`
	Notice         Asset    `json:"notice"`
	SPDX           Asset    `json:"spdx"`
}

type Catalog struct {
	SchemaVersion int      `json:"schema_version"`
	Helpers       []Helper `json:"helpers"`
}

func LoadCatalog(raw []byte) (Catalog, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var catalog Catalog
	if err := decoder.Decode(&catalog); err != nil {
		return Catalog{}, fmt.Errorf("decode connect helper catalog: %w", err)
	}
	if decoder.More() {
		return Catalog{}, errors.New("connect helper catalog contains trailing data")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Catalog{}, err
	}
	return NewCatalog(catalog)
}

func NewCatalog(catalog Catalog) (Catalog, error) {
	if catalog.SchemaVersion != CatalogSchemaVersion {
		return Catalog{}, fmt.Errorf("unsupported connect helper catalog schema %d", catalog.SchemaVersion)
	}
	if len(catalog.Helpers) == 0 {
		return Catalog{}, errors.New("connect helper catalog has no helpers")
	}
	helpers := append([]Helper(nil), catalog.Helpers...)
	seenHelpers := map[string]bool{}
	seenMirrors := map[string]bool{}
	for index := range helpers {
		helper, err := normalizeHelper(helpers[index], seenMirrors)
		if err != nil {
			return Catalog{}, fmt.Errorf("helper %d: %w", index, err)
		}
		key := helper.ID + "|" + helper.Platform
		if seenHelpers[key] {
			return Catalog{}, errors.New("connect helper catalog contains a duplicate helper/platform")
		}
		seenHelpers[key] = true
		helpers[index] = helper
	}
	slices.SortFunc(helpers, func(left, right Helper) int {
		if value := strings.Compare(left.ID, right.ID); value != 0 {
			return value
		}
		return strings.Compare(left.Platform, right.Platform)
	})
	catalog.Helpers = helpers
	return catalog, nil
}

func EncodeCatalog(catalog Catalog) ([]byte, error) {
	normalized, err := NewCatalog(catalog)
	if err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func (helper Helper) Assets() []Asset {
	return []Asset{helper.Archive, helper.LicenseReport, helper.Notice, helper.SPDX}
}

func normalizeHelper(helper Helper, seenMirrors map[string]bool) (Helper, error) {
	helper.ID = strings.TrimSpace(helper.ID)
	helper.Provider = strings.TrimSpace(helper.Provider)
	helper.Version = strings.TrimSpace(helper.Version)
	helper.Platform = strings.TrimSpace(helper.Platform)
	helper.Executable = strings.TrimSpace(helper.Executable)
	if !catalogTokenPattern.MatchString(helper.ID) || !catalogTokenPattern.MatchString(helper.Provider) ||
		!catalogTokenPattern.MatchString(helper.Platform) || !catalogVersionPattern.MatchString(helper.Version) {
		return Helper{}, errors.New("helper identity is invalid")
	}
	if err := validateArchiveName(helper.Executable); err != nil {
		return Helper{}, fmt.Errorf("executable: %w", err)
	}
	if len(helper.ArchiveMembers) == 0 {
		return Helper{}, errors.New("archive member allowlist is empty")
	}
	members := append([]string(nil), helper.ArchiveMembers...)
	memberSeen := map[string]bool{}
	hasExecutable := false
	for _, member := range members {
		if err := validateArchiveName(member); err != nil {
			return Helper{}, fmt.Errorf("archive member %q: %w", member, err)
		}
		if memberSeen[member] {
			return Helper{}, errors.New("archive member allowlist contains duplicates")
		}
		memberSeen[member] = true
		hasExecutable = hasExecutable || member == helper.Executable
	}
	if !hasExecutable {
		return Helper{}, errors.New("archive member allowlist does not contain the helper executable")
	}
	slices.Sort(members)
	helper.ArchiveMembers = members

	assets := []*Asset{&helper.Archive, &helper.LicenseReport, &helper.Notice, &helper.SPDX}
	for _, asset := range assets {
		normalized, err := normalizeAsset(*asset)
		if err != nil {
			return Helper{}, err
		}
		if seenMirrors[normalized.MirrorAsset] {
			return Helper{}, errors.New("connect helper mirror asset names must be unique")
		}
		seenMirrors[normalized.MirrorAsset] = true
		*asset = normalized
	}
	return helper, nil
}

func normalizeAsset(asset Asset) (Asset, error) {
	asset.SourceURL = strings.TrimSpace(asset.SourceURL)
	asset.SourceSHA256 = strings.TrimSpace(asset.SourceSHA256)
	asset.MirrorAsset = strings.TrimSpace(asset.MirrorAsset)
	parsed, err := url.Parse(asset.SourceURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || strings.Contains(parsed.Path, "/latest/") {
		return Asset{}, errors.New("connect helper source URL is not an exact HTTPS artifact URL")
	}
	if !catalogSHA256Pattern.MatchString(asset.SourceSHA256) ||
		asset.SourceLength <= 0 || asset.SourceLength > maxHelperAssetBytes {
		return Asset{}, errors.New("connect helper source identity is invalid")
	}
	if err := validateArchiveName(asset.MirrorAsset); err != nil {
		return Asset{}, fmt.Errorf("mirror asset: %w", err)
	}
	return asset, nil
}

func validateArchiveName(value string) error {
	if value == "" || value != path.Base(value) || value == "." || value == ".." ||
		strings.ContainsAny(value, "/\\\x00") {
		return errors.New("name must be a plain non-empty filename")
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return errors.New("connect helper catalog contains trailing JSON")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode connect helper catalog trailing data: %w", err)
	}
	return nil
}
