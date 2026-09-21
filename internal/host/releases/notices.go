package releases

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	NoticeBundleVersion  = 1
	maxNoticeBundleBytes = 64 << 20
	maxNoticeFileBytes   = 8 << 20
)

var noticeComponentPattern = regexp.MustCompile("^[a-z0-9][a-z0-9+._-]{0,127}$")

type NoticeRequirement struct {
	Component      string
	Version        string
	NoticeRequired bool
}

type NoticeMaterial struct {
	Component string
	Version   string
	Kind      string
	Filename  string
	Data      []byte
}

type NoticeEntry struct {
	Component string `json:"component"`
	Version   string `json:"version"`
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	Length    int64  `json:"length"`
	SHA256    string `json:"sha256"`
}

type NoticeManifest struct {
	Version int           `json:"version"`
	Entries []NoticeEntry `json:"entries"`
}

type normalizedNoticeMaterial struct {
	entry NoticeEntry
	data  []byte
}

type observedNoticeFile struct {
	length int64
	sha256 string
}

func BuildNoticeBundle(requirements []NoticeRequirement, materials []NoticeMaterial) ([]byte, NoticeManifest, error) {
	required, err := normalizeNoticeRequirements(requirements)
	if err != nil {
		return nil, NoticeManifest{}, err
	}
	normalized, err := normalizeNoticeMaterials(required, materials)
	if err != nil {
		return nil, NoticeManifest{}, err
	}
	entries := make([]NoticeEntry, 0, len(normalized))
	for _, material := range normalized {
		entries = append(entries, material.entry)
	}
	manifest := NoticeManifest{Version: NoticeBundleVersion, Entries: entries}
	if err = validateNoticeManifest(manifest); err != nil {
		return nil, NoticeManifest{}, err
	}
	if err = validateNoticeCoverage(required, manifest.Entries); err != nil {
		return nil, NoticeManifest{}, err
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		return nil, NoticeManifest{}, err
	}

	var output bytes.Buffer
	gzipWriter, err := gzip.NewWriterLevel(&output, gzip.BestCompression)
	if err != nil {
		return nil, NoticeManifest{}, err
	}
	gzipWriter.Header.ModTime = time.Unix(0, 0).UTC()
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	if err = writeNoticeTarFile(tarWriter, "manifest.json", manifestRaw); err != nil {
		return nil, NoticeManifest{}, closeNoticeWriters(tarWriter, gzipWriter, err)
	}
	for _, material := range normalized {
		if err = writeNoticeTarFile(tarWriter, material.entry.Path, material.data); err != nil {
			return nil, NoticeManifest{}, closeNoticeWriters(tarWriter, gzipWriter, err)
		}
	}
	if err = tarWriter.Close(); err != nil {
		_ = gzipWriter.Close()
		return nil, NoticeManifest{}, err
	}
	if err = gzipWriter.Close(); err != nil {
		return nil, NoticeManifest{}, err
	}
	if output.Len() > maxNoticeBundleBytes {
		return nil, NoticeManifest{}, errors.New("notice bundle exceeds size policy")
	}
	return output.Bytes(), manifest, nil
}

func VerifyNoticeBundle(raw []byte, requirements []NoticeRequirement) (NoticeManifest, error) {
	if len(raw) == 0 || len(raw) > maxNoticeBundleBytes {
		return NoticeManifest{}, errors.New("notice bundle exceeds size policy")
	}
	required, err := normalizeNoticeRequirements(requirements)
	if err != nil {
		return NoticeManifest{}, err
	}
	gzipReader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return NoticeManifest{}, fmt.Errorf("open notice bundle: %w", err)
	}
	defer gzipReader.Close()
	gzipReader.Multistream(false)
	tarReader := tar.NewReader(gzipReader)

	var manifestRaw []byte
	observed := map[string]observedNoticeFile{}
	var total int64
	for {
		header, nextErr := tarReader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return NoticeManifest{}, fmt.Errorf("read notice bundle: %w", nextErr)
		}
		if header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > maxNoticeFileBytes {
			return NoticeManifest{}, errors.New("notice bundle contains an invalid file entry")
		}
		if header.Name == "" || path.IsAbs(header.Name) || path.Clean(header.Name) != header.Name ||
			header.Name == ".." || strings.HasPrefix(header.Name, "../") || strings.ContainsRune(header.Name, 0) {
			return NoticeManifest{}, errors.New("notice bundle contains an unsafe path")
		}
		if _, exists := observed[header.Name]; exists || header.Name == "manifest.json" && manifestRaw != nil {
			return NoticeManifest{}, errors.New("notice bundle contains duplicate files")
		}
		if total > maxNoticeBundleBytes-header.Size {
			return NoticeManifest{}, errors.New("notice bundle expanded content exceeds size policy")
		}
		data, readErr := io.ReadAll(io.LimitReader(tarReader, header.Size+1))
		if readErr != nil || int64(len(data)) != header.Size {
			return NoticeManifest{}, errors.New("notice bundle file length is invalid")
		}
		total += header.Size
		if header.Name == "manifest.json" {
			manifestRaw = data
			continue
		}
		sum := sha256.Sum256(data)
		observed[header.Name] = observedNoticeFile{length: header.Size, sha256: hex.EncodeToString(sum[:])}
	}
	if len(manifestRaw) == 0 {
		return NoticeManifest{}, errors.New("notice bundle manifest is missing")
	}
	var manifest NoticeManifest
	if err = decodeStrictJSON(manifestRaw, &manifest, "notice bundle manifest"); err != nil {
		return NoticeManifest{}, err
	}
	if err = validateNoticeManifest(manifest); err != nil {
		return NoticeManifest{}, err
	}
	if len(observed) != len(manifest.Entries) {
		return NoticeManifest{}, errors.New("notice bundle files do not match the manifest")
	}
	for _, entry := range manifest.Entries {
		file, ok := observed[entry.Path]
		if !ok || file.length != entry.Length || file.sha256 != entry.SHA256 {
			return NoticeManifest{}, fmt.Errorf("notice bundle file %q does not match its manifest identity", entry.Path)
		}
	}
	if err = validateNoticeCoverage(required, manifest.Entries); err != nil {
		return NoticeManifest{}, err
	}
	return manifest, nil
}

func normalizeNoticeRequirements(requirements []NoticeRequirement) (map[string]NoticeRequirement, error) {
	if len(requirements) == 0 {
		return nil, errors.New("notice requirements are empty")
	}
	result := make(map[string]NoticeRequirement, len(requirements))
	for _, requirement := range requirements {
		requirement.Component = strings.ToLower(strings.TrimSpace(requirement.Component))
		requirement.Version = strings.TrimSpace(requirement.Version)
		if !noticeComponentPattern.MatchString(requirement.Component) ||
			requirement.Version == "" || len(requirement.Version) > 256 ||
			strings.ContainsAny(requirement.Version, "\r\n\x00") {
			return nil, errors.New("notice requirement identity is invalid")
		}
		if _, exists := result[requirement.Component]; exists {
			return nil, fmt.Errorf("duplicate notice requirement %q", requirement.Component)
		}
		result[requirement.Component] = requirement
	}
	return result, nil
}

func normalizeNoticeMaterials(required map[string]NoticeRequirement, materials []NoticeMaterial) ([]normalizedNoticeMaterial, error) {
	if len(materials) == 0 {
		return nil, errors.New("notice materials are empty")
	}
	result := make([]normalizedNoticeMaterial, 0, len(materials))
	seen := map[string]bool{}
	for _, material := range materials {
		material.Component = strings.ToLower(strings.TrimSpace(material.Component))
		material.Version = strings.TrimSpace(material.Version)
		material.Kind = strings.ToLower(strings.TrimSpace(material.Kind))
		material.Filename = strings.TrimSpace(material.Filename)
		requirement, ok := required[material.Component]
		if !ok || material.Version != requirement.Version {
			return nil, fmt.Errorf("notice material %q is not in the required release inventory", material.Component)
		}
		if material.Kind != "license" && material.Kind != "notice" {
			return nil, errors.New("notice material kind must be license or notice")
		}
		if material.Filename == "" || path.Base(material.Filename) != material.Filename ||
			strings.ContainsAny(material.Filename, "\r\n\x00\\") {
			return nil, errors.New("notice material filename is invalid")
		}
		if len(material.Data) == 0 || len(material.Data) > maxNoticeFileBytes {
			return nil, errors.New("notice material content exceeds size policy")
		}
		prefix := "licenses"
		if material.Kind == "notice" {
			prefix = "notices"
		}
		archivePath := path.Join(prefix, material.Component, material.Filename)
		if seen[archivePath] {
			return nil, fmt.Errorf("duplicate notice material path %q", archivePath)
		}
		seen[archivePath] = true
		sum := sha256.Sum256(material.Data)
		result = append(result, normalizedNoticeMaterial{
			entry: NoticeEntry{
				Component: material.Component,
				Version:   material.Version,
				Kind:      material.Kind,
				Path:      archivePath,
				Length:    int64(len(material.Data)),
				SHA256:    hex.EncodeToString(sum[:]),
			},
			data: append([]byte(nil), material.Data...),
		})
	}
	slices.SortFunc(result, func(left, right normalizedNoticeMaterial) int {
		return strings.Compare(left.entry.Path, right.entry.Path)
	})
	return result, nil
}

func validateNoticeManifest(manifest NoticeManifest) error {
	if manifest.Version != NoticeBundleVersion {
		return fmt.Errorf("unsupported notice bundle version %d", manifest.Version)
	}
	if len(manifest.Entries) == 0 {
		return errors.New("notice bundle manifest has no entries")
	}
	previous := ""
	for _, entry := range manifest.Entries {
		if !noticeComponentPattern.MatchString(entry.Component) ||
			entry.Version == "" || len(entry.Version) > 256 || strings.ContainsAny(entry.Version, "\r\n\x00") ||
			entry.Kind != "license" && entry.Kind != "notice" ||
			entry.Length <= 0 || entry.Length > maxNoticeFileBytes ||
			!sha256Pattern.MatchString(entry.SHA256) {
			return errors.New("notice bundle manifest entry is invalid")
		}
		filename := path.Base(entry.Path)
		prefix := "licenses"
		if entry.Kind == "notice" {
			prefix = "notices"
		}
		if filename == "." || filename == "/" || entry.Path != path.Join(prefix, entry.Component, filename) {
			return errors.New("notice bundle manifest path is invalid")
		}
		if entry.Path <= previous {
			return errors.New("notice bundle manifest entries must be uniquely sorted by path")
		}
		previous = entry.Path
	}
	return nil
}

func validateNoticeCoverage(required map[string]NoticeRequirement, entries []NoticeEntry) error {
	type coverage struct {
		version string
		license bool
		notice  bool
	}
	seen := make(map[string]coverage, len(required))
	for _, entry := range entries {
		requirement, ok := required[entry.Component]
		if !ok || requirement.Version != entry.Version {
			return fmt.Errorf("notice bundle contains unrequired component %q", entry.Component)
		}
		current := seen[entry.Component]
		current.version = entry.Version
		if entry.Kind == "license" {
			current.license = true
		} else {
			current.notice = true
		}
		seen[entry.Component] = current
	}
	for component, requirement := range required {
		current := seen[component]
		if !current.license {
			return fmt.Errorf("required license material for %q is missing", component)
		}
		if requirement.NoticeRequired && !current.notice {
			return fmt.Errorf("required NOTICE material for %q is missing", component)
		}
	}
	return nil
}

func writeNoticeTarFile(writer *tar.Writer, name string, data []byte) error {
	header := &tar.Header{
		Name:     name,
		Mode:     0644,
		Size:     int64(len(data)),
		ModTime:  time.Unix(0, 0).UTC(),
		Typeflag: tar.TypeReg,
	}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	_, err := writer.Write(data)
	return err
}

func closeNoticeWriters(tarWriter *tar.Writer, gzipWriter *gzip.Writer, primary error) error {
	tarErr := tarWriter.Close()
	gzipErr := gzipWriter.Close()
	return errors.Join(primary, tarErr, gzipErr)
}
