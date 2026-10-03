package tools

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
)

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Artifact is supplied by a trusted release catalog. A checksum obtained from
// the same untrusted download is not a release trust root.
type Artifact struct {
	Module  ID     `json:"module"`
	Release string `json:"release"`
	Target  Target `json:"target"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Bytes   int64  `json:"bytes"`
	Format  string `json:"format"`
}

func (a Artifact) Validate() error {
	if !validID(a.Module) || !releasePattern.MatchString(a.Release) {
		return fmt.Errorf("artifact requires a valid module and release")
	}
	if err := a.Target.Validate(); err != nil {
		return err
	}
	u, err := url.Parse(a.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("artifact requires an HTTPS acquisition URL without credentials or fragment")
	}
	if !digestPattern.MatchString(a.SHA256) || a.Bytes <= 0 || a.Bytes > 16<<30 {
		return fmt.Errorf("artifact requires an exact SHA-256 digest and positive byte length")
	}
	if a.Format != "tar.gz" && a.Format != "zip" {
		return fmt.Errorf("unsupported artifact format %q", a.Format)
	}
	return nil
}

// Identity remains stable when a download mirror changes.
func (a Artifact) Identity() string {
	return fmt.Sprintf("%s/%s/%s/%s/%s/sha256:%s", a.Module, a.Release, a.Target.OS, a.Target.Arch, a.Target.Mode, a.SHA256)
}

func (a Artifact) Digest() ([]byte, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return hex.DecodeString(a.SHA256)
}

// Artifacts returns the exact target/release closure in dependency order.
func (r Resolution) Artifacts(catalog []Artifact) ([]Artifact, error) {
	byModule := map[ID]Artifact{}
	for _, a := range catalog {
		if err := a.Validate(); err != nil {
			return nil, err
		}
		if a.Target != r.Target {
			continue
		}
		if _, exists := byModule[a.Module]; exists {
			return nil, fmt.Errorf("duplicate artifact for %s", a.Module)
		}
		byModule[a.Module] = a
	}
	var artifacts []Artifact
	for _, m := range r.Ordered {
		a, exists := byModule[m.ID]
		if !exists || a.Release != m.Release {
			return nil, fmt.Errorf("missing release-bound artifact for %s", m.ID)
		}
		artifacts = append(artifacts, a)
	}
	return artifacts, nil
}
