package mcpserver

import (
	"errors"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/config"
)

type ResourceOrigins struct{ ArtifactBaseURL, PreviewDomain string }

func (o ResourceOrigins) validate() error {
	if o.ArtifactBaseURL != "" {
		u, err := url.Parse(o.ArtifactBaseURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
			return errors.New("invalid artifact widget origin")
		}
	}
	if o.PreviewDomain != "" && !config.Hostname(o.PreviewDomain) {
		return errors.New("invalid preview widget domain")
	}
	return nil
}
func (o ResourceOrigins) metadata(uri string) mcp.Meta {
	var domain any
	resources, frames := []string{}, []string{}
	if o.ArtifactBaseURL != "" {
		u, _ := url.Parse(o.ArtifactBaseURL)
		domain = u.Scheme + "://" + u.Host
		resources = append(resources, domain.(string))
	}
	if o.PreviewDomain != "" {
		frames = append(frames, "https://*."+o.PreviewDomain)
	}
	uiCSP := map[string]any{}
	legacyCSP := map[string]any{"resource_domains": []string{}, "connect_domains": []string{}}
	switch {
	case strings.Contains(uri, "/image-viewer-"):
		uiCSP = map[string]any{"resourceDomains": resources}
		legacyCSP = map[string]any{"resource_domains": resources}
	case strings.Contains(uri, "/live-preview-"):
		uiCSP = map[string]any{"frameDomains": frames}
		legacyCSP = map[string]any{"frame_domains": frames}
	}
	return mcp.Meta{"ui": map[string]any{"prefersBorder": true, "domain": domain, "csp": uiCSP}, "openai/widgetPrefersBorder": true, "openai/widgetDomain": domain, "openai/widgetCSP": legacyCSP}
}
