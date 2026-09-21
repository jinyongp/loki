package service

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/fault"
	"loki/internal/mcpserver"
	"loki/internal/workspace"
)

type workspaceRead struct {
	Action, Path  string
	Query         *string
	MaxDepth      int `json:"max_depth"`
	Offset, Limit int
	MaxResults    int `json:"max_results"`
	Regex         bool
	Revision      *string
}
type workspaceEdit struct {
	Action                                              string
	Path, Content, Old, New, Patch, Source, Destination *string
	ExpectedSHA256                                      *string `json:"expected_sha256"`
	ExpectedDestination                                 *string `json:"expected_destination"`
	ExpectedReplacements                                int     `json:"expected_replacements"`
	RequestID                                           *string `json:"request_id"`
	Operations                                          []workspaceBatchOperation
}
type workspaceBatchOperation struct {
	Action               string
	Path                 string
	Content              string
	Old                  string
	New                  string
	ExpectedSHA256       string `json:"expected_sha256"`
	ExpectedReplacements int    `json:"expected_replacements"`
	Source               string
	Destination          string
	ExpectedDestination  string `json:"expected_destination"`
}
type imageWrite struct {
	Path      string
	Data      string `json:"data_base64"`
	MIME      string `json:"mime_type"`
	Overwrite bool
	Expected  string `json:"expected_sha256"`
}
type filePath struct{ Path string }
type fileRestore struct {
	Path, Revision string
	Expected       string `json:"expected_sha256"`
}
type fileRemove struct {
	Path     string
	Expected string `json:"expected_sha256"`
}

func objectResult(value map[string]any, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return nil, err
	}
	return mcpserver.Object(value)
}

func WorkspaceHandlers(files *workspace.Files) map[string]mcpserver.Handler {
	return map[string]mcpserver.Handler{
		"workspace_read": mcpserver.Typed(func(ctx context.Context, r workspaceRead) (*mcp.CallToolResult, error) {
			switch r.Action {
			case "list":
				return objectResult(files.List(ctx, r.Path, r.MaxDepth, r.Offset, r.Limit))
			case "file":
				return objectResult(files.Read(r.Path, r.Offset, r.Limit))
			case "search":
				query, err := mcpserver.Require(r.Query, "query")
				if err != nil {
					return nil, err
				}
				return objectResult(files.Search(ctx, query, r.Path, r.MaxResults, r.Regex))
			case "revisions":
				return objectResult(files.Revisions(r.Path, r.Limit))
			case "revision_diff":
				revision, err := mcpserver.Require(r.Revision, "revision")
				if err != nil {
					return nil, err
				}
				return objectResult(files.RevisionDiff(r.Path, revision))
			}
			return nil, fault.Error("invalid workspace read action")
		}),
		"workspace_edit": mcpserver.Typed(func(ctx context.Context, r workspaceEdit) (*mcp.CallToolResult, error) {
			value := func(v *string, n string) (string, error) { return mcpserver.Require(v, n) }
			switch r.Action {
			case "create":
				path, err := value(r.Path, "path")
				if err != nil {
					return nil, err
				}
				content, err := value(r.Content, "content")
				if err != nil {
					return nil, err
				}
				return objectResult(files.Create(path, content))
			case "replace":
				path, err := value(r.Path, "path")
				if err != nil {
					return nil, err
				}
				old, err := value(r.Old, "old")
				if err != nil {
					return nil, err
				}
				new, err := value(r.New, "new")
				if err != nil {
					return nil, err
				}
				expected, err := value(r.ExpectedSHA256, "expected_sha256")
				if err != nil {
					return nil, err
				}
				return objectResult(files.Replace(path, old, new, expected, r.ExpectedReplacements))
			case "patch":
				patch, err := value(r.Patch, "patch")
				if err != nil {
					return nil, err
				}
				return objectResult(files.Patch(ctx, patch))
			case "move":
				source, err := value(r.Source, "source")
				if err != nil {
					return nil, err
				}
				dest, err := value(r.Destination, "destination")
				if err != nil {
					return nil, err
				}
				expected, err := value(r.ExpectedSHA256, "expected_sha256")
				if err != nil {
					return nil, err
				}
				expectedDestination, err := value(r.ExpectedDestination, "expected_destination")
				if err != nil {
					return nil, err
				}
				return objectResult(files.Move(source, dest, expected, expectedDestination))
			case "batch":
				requestID, err := value(r.RequestID, "request_id")
				if err != nil {
					return nil, err
				}
				operations := make([]workspace.BatchOperation, len(r.Operations))
				for index, operation := range r.Operations {
					operations[index] = workspace.BatchOperation{
						Action: operation.Action, Path: operation.Path, Content: operation.Content,
						Old: operation.Old, New: operation.New, ExpectedSHA256: operation.ExpectedSHA256,
						ExpectedReplacements: operation.ExpectedReplacements, Source: operation.Source,
						Destination: operation.Destination, ExpectedDestination: operation.ExpectedDestination,
					}
				}
				return objectResult(files.Batch(ctx, requestID, operations))
			}
			return nil, fault.Error("invalid workspace edit action")
		}),
		"read_image": mcpserver.Typed(func(_ context.Context, r filePath) (*mcp.CallToolResult, error) {
			data, metadata, err := files.Image(r.Path)
			if err != nil {
				return nil, err
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: data, MIMEType: metadata["mime_type"].(string)}}, StructuredContent: metadata}, nil
		}),
		"write_image": mcpserver.Typed(func(_ context.Context, r imageWrite) (*mcp.CallToolResult, error) {
			return objectResult(files.WriteImage(r.Path, r.Data, r.MIME, r.Overwrite, r.Expected))
		}),
		"restore_workspace_file": mcpserver.Typed(func(_ context.Context, r fileRestore) (*mcp.CallToolResult, error) {
			return objectResult(files.Restore(r.Path, r.Revision, r.Expected))
		}),
		"remove_tracked_file": mcpserver.Typed(func(ctx context.Context, r fileRemove) (*mcp.CallToolResult, error) {
			return objectResult(files.RemoveTracked(ctx, r.Path, r.Expected))
		}),
	}
}
