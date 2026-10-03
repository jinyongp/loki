package toolproxy

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// These resources are the protected browser service's own file adapter, not
// invented upstream engine capabilities. Their native contents are preserved.
type ownedResourceProxy struct {
	server   *mcp.Server
	owners   *bindingOwners
	options  Options
	mu       sync.Mutex
	previous []string
	closed   bool
}

func (r *ownedResourceProxy) clearLocked() {
	_ = r.owners.registry.Replace("resources", r.options.Owner, nil, func() {
		r.server.RemoveResourceTemplates(r.previous...)
		r.previous = nil
	})
}

func (r *ownedResourceProxy) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.clearLocked()
}

func (r *ownedResourceProxy) refresh(ctx context.Context, engine *mcp.ClientSession) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	if r.options.AuthorizeResource() != nil {
		r.clearLocked()
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var templates []*mcp.ResourceTemplate
	var names []string
	for template, err := range engine.ResourceTemplates(ctx, nil) {
		if err != nil {
			r.clearLocked()
			return err
		}
		if len(templates) >= 128 || !strings.HasPrefix(template.URITemplate, "loki://browser/files/") || !strings.HasSuffix(template.URITemplate, "/{+path}") {
			r.clearLocked()
			return fmt.Errorf("protected browser returned an unsupported owned resource template")
		}
		templates = append(templates, template)
		names = append(names, template.URITemplate)
	}
	return r.owners.registry.Replace("resources", r.options.Owner, names, func() {
		r.server.RemoveResourceTemplates(r.previous...)
		r.previous = nil
		for _, template := range templates {
			definition := *template
			r.server.AddResourceTemplate(&definition, func(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				if err := r.options.AuthorizeResource(); err != nil {
					return nil, err
				}
				ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				defer cancel()
				return engine.ReadResource(ctx, request.Params)
			})
			r.previous = append(r.previous, definition.URITemplate)
		}
	})
}
