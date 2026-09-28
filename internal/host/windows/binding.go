package windows

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"loki/internal/buildinfo"
)

const ReleaseBindingSchemaVersion = 1

var (
	WSLApplianceSHA256  = ""
	WSLApplianceLength  = ""
	HelperCatalogSHA256 = ""
	HelperCatalogLength = ""
)

var bindingDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type FileBinding struct {
	SHA256 string `json:"sha256"`
	Length int64  `json:"length"`
}

type ReleaseBinding struct {
	SchemaVersion  int         `json:"schema_version"`
	ReleaseTag     string      `json:"release_tag"`
	SourceRevision string      `json:"source_revision"`
	BuiltAt        string      `json:"built_at"`
	WSLAppliance   FileBinding `json:"wsl_appliance"`
	HelperCatalog  FileBinding `json:"helper_catalog"`
}

func CurrentReleaseBinding() (ReleaseBinding, error) {
	wslLength, err := parseBoundLength(WSLApplianceLength)
	if err != nil {
		return ReleaseBinding{}, errors.New("Windows frontend WSL appliance binding is invalid")
	}
	catalogLength, err := parseBoundLength(HelperCatalogLength)
	if err != nil {
		return ReleaseBinding{}, errors.New("Windows frontend helper catalog binding is invalid")
	}
	binding := ReleaseBinding{
		SchemaVersion:  ReleaseBindingSchemaVersion,
		ReleaseTag:     "v" + buildinfo.Version,
		SourceRevision: buildinfo.Commit,
		BuiltAt:        buildinfo.Date,
		WSLAppliance: FileBinding{
			SHA256: WSLApplianceSHA256,
			Length: wslLength,
		},
		HelperCatalog: FileBinding{
			SHA256: HelperCatalogSHA256,
			Length: catalogLength,
		},
	}
	if err = ValidateReleaseBinding(binding); err != nil {
		return ReleaseBinding{}, err
	}
	return binding, nil
}

func ValidateReleaseBinding(binding ReleaseBinding) error {
	if binding.SchemaVersion != ReleaseBindingSchemaVersion ||
		!validFrontendReleaseTag(binding.ReleaseTag) ||
		!frontendRevisionPattern.MatchString(strings.TrimSpace(binding.SourceRevision)) ||
		!bindingDigestPattern.MatchString(binding.WSLAppliance.SHA256) || binding.WSLAppliance.Length <= 0 ||
		!bindingDigestPattern.MatchString(binding.HelperCatalog.SHA256) || binding.HelperCatalog.Length <= 0 {
		return errors.New("Windows frontend release binding is invalid")
	}
	if _, err := time.Parse(time.RFC3339, strings.TrimSpace(binding.BuiltAt)); err != nil {
		return errors.New("Windows frontend release timestamp is invalid")
	}
	return nil
}

func parseBoundLength(raw string) (int64, error) {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New("invalid bound file length")
	}
	return value, nil
}
