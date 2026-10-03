package toolproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxFileBytes = 4 << 20
const maxResultFiles = 4096

type fileSession struct {
	directory string
	root      *os.Root
	prefix    string
	authorize func() error
	template  *mcp.ResourceTemplate
	handler   mcp.ResourceHandler
}

type fileEntry struct {
	Name     string `json:"name"`
	URI      string `json:"uri"`
	Bytes    int64  `json:"bytes"`
	MIMEType string `json:"mime_type"`
	HostPath string `json:"execution_host_path"`
}

func localFile(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, "\\:\x00") || !filepath.IsLocal(filepath.FromSlash(name)) || filepath.ToSlash(filepath.Clean(filepath.FromSlash(name))) != name || name == "." {
		return "", fmt.Errorf("file name must be a canonical relative path within this browser session")
	}
	return filepath.FromSlash(name), nil
}

func fileRootURI(directory string) string {
	path := filepath.ToSlash(directory)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func (f *fileSession) entry(name string, info os.FileInfo) fileEntry {
	contentType := mime.TypeByExtension(filepath.Ext(name))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return fileEntry{Name: name, URI: f.prefix + url.PathEscape(name), Bytes: info.Size(), MIMEType: contentType, HostPath: filepath.Join(f.directory, filepath.FromSlash(name))}
}

func (f *fileSession) list() ([]fileEntry, error) {
	if err := f.authorize(); err != nil {
		return nil, err
	}
	entries := []fileEntry{}
	err := filepath.WalkDir(f.directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		name, err := filepath.Rel(f.directory, path)
		if err != nil {
			return err
		}
		if len(entries) >= maxResultFiles {
			return fmt.Errorf("browser result listing exceeds %d files; narrow or clean this session", maxResultFiles)
		}
		entries = append(entries, f.entry(filepath.ToSlash(name), info))
		return nil
	})
	return entries, err
}

func (f *fileSession) read(name string) (fileEntry, []byte, error) {
	if err := f.authorize(); err != nil {
		return fileEntry{}, nil, err
	}
	path, err := localFile(name)
	if err != nil {
		return fileEntry{}, nil, err
	}
	file, err := f.root.Open(path)
	if err != nil {
		return fileEntry{}, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fileEntry{}, nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileBytes {
		return fileEntry{}, nil, fmt.Errorf("browser file must be regular and at most %d bytes", maxFileBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		return fileEntry{}, nil, err
	}
	if len(data) > maxFileBytes {
		return fileEntry{}, nil, fmt.Errorf("browser file grew beyond transfer limit")
	}
	return f.entry(name, info), data, nil
}

func (f *fileSession) stage(name, encoded string) (fileEntry, error) {
	if err := f.authorize(); err != nil {
		return fileEntry{}, err
	}
	path, err := localFile(name)
	if err != nil || filepath.Base(path) != path {
		return fileEntry{}, fmt.Errorf("upload name must be a single filename")
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(maxFileBytes) {
		return fileEntry{}, fmt.Errorf("upload exceeds transfer limit")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(data) > maxFileBytes {
		return fileEntry{}, fmt.Errorf("upload requires valid base64 within transfer limit")
	}
	if err := f.root.MkdirAll("inputs", 0700); err != nil {
		return fileEntry{}, err
	}
	path = filepath.Join("inputs", path)
	file, err := f.root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fileEntry{}, err
	}
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if writeErr != nil || statErr != nil || closeErr != nil {
		_ = f.root.Remove(path)
		if writeErr != nil {
			return fileEntry{}, writeErr
		}
		if statErr != nil {
			return fileEntry{}, statErr
		}
		return fileEntry{}, closeErr
	}
	return f.entry(filepath.ToSlash(path), info), nil
}

func registerFiles(ctx context.Context, server *mcp.Server, owners *bindingOwners, options []Options) (func(), error) {
	sessions := map[string]*fileSession{}
	closeAll := func() {
		for _, session := range sessions {
			_ = session.root.Close()
		}
	}
	for _, option := range options {
		if option.Results == "" {
			continue
		}
		if option.AuthorizeResource == nil {
			closeAll()
			return nil, fmt.Errorf("browser files require a current-state authorizer")
		}
		info, err := os.Lstat(option.Results)
		if err != nil {
			closeAll()
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			closeAll()
			return nil, fmt.Errorf("browser result root must be a real directory")
		}
		root, err := os.OpenRoot(option.Results)
		if err != nil {
			closeAll()
			return nil, err
		}
		owner := option.Owner
		if owner == "" {
			owner = option.Name
		}
		if owner == "" || sessions[owner] != nil {
			root.Close()
			closeAll()
			return nil, fmt.Errorf("browser file sessions require unique owners")
		}
		digest := sha256.Sum256([]byte(option.Results))
		session := &fileSession{directory: option.Results, root: root, prefix: fmt.Sprintf("loki://browser/files/%s/%x/", url.PathEscape(owner), digest[:16]), authorize: option.AuthorizeResource}
		sessions[owner] = session
		session.template = &mcp.ResourceTemplate{Name: owner + " session files", URITemplate: session.prefix + "{+path}", Description: "Files created or staged in this browser session on the execution host."}
		session.handler = func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			if !strings.HasPrefix(req.Params.URI, session.prefix) {
				return nil, fmt.Errorf("resource is outside this browser session")
			}
			name, err := url.PathUnescape(strings.TrimPrefix(req.Params.URI, session.prefix))
			if err != nil {
				return nil, err
			}
			entry, data, err := session.read(name)
			if err != nil {
				return nil, err
			}
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: entry.URI, MIMEType: entry.MIMEType, Blob: data}}}, nil
		}
	}
	if len(sessions) == 0 {
		return closeAll, nil
	}
	engineNames := make([]string, 0, len(sessions))
	for name := range sessions {
		engineNames = append(engineNames, name)
	}
	slices.Sort(engineNames)
	tool := &mcp.Tool{Name: "loki_browser_files", Description: "Transfer files for the selected execution-host browser session. list returns result URIs and host paths; read returns a file or inline image; stage accepts base64 and returns a host path for the official browser upload tool. File operations stay inside owned session directories.", InputSchema: map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"engine", "action"},
		"properties": map[string]any{"engine": map[string]any{"type": "string", "enum": engineNames}, "action": map[string]any{"type": "string", "enum": []string{"list", "read", "stage"}}, "name": map[string]any{"type": "string"}, "data": map[string]any{"type": "string", "description": "Base64 file content for stage, at most 4 MiB decoded."}},
	}}
	apply := func() {
		server.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var args struct {
				Engine string `json:"engine"`
				Action string `json:"action"`
				Name   string `json:"name"`
				Data   string `json:"data"`
			}
			if len(req.Params.Arguments) > base64.StdEncoding.EncodedLen(maxFileBytes)+4096 {
				return nil, fmt.Errorf("browser file request exceeds transfer limit")
			}
			decoder := json.NewDecoder(bytes.NewReader(req.Params.Arguments))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&args); err != nil {
				return nil, err
			}
			session := sessions[args.Engine]
			if session == nil {
				return nil, fmt.Errorf("unknown browser file session")
			}
			var payload any
			switch args.Action {
			case "list":
				entries, err := session.list()
				if err != nil {
					return nil, err
				}
				payload = map[string]any{"directory": session.directory, "files": entries}
			case "stage":
				entry, err := session.stage(args.Name, args.Data)
				if err != nil {
					return nil, err
				}
				payload = entry
			case "read":
				entry, data, err := session.read(args.Name)
				if err != nil {
					return nil, err
				}
				if entry.MIMEType == "image/png" || entry.MIMEType == "image/jpeg" || entry.MIMEType == "image/webp" {
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{MIMEType: entry.MIMEType, Data: data}}}, nil
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: entry.URI, MIMEType: entry.MIMEType, Blob: data}}}}, nil
			default:
				return nil, fmt.Errorf("unknown browser file action")
			}
			data, err := json.Marshal(payload)
			if err != nil {
				return nil, err
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil
		})
	}
	refresh := func() error {
		anyEnabled := false
		for _, session := range sessions {
			server.RemoveResourceTemplates(session.template.URITemplate)
			if session.authorize() == nil {
				server.AddResourceTemplate(session.template, session.handler)
				anyEnabled = true
			}
		}
		if !anyEnabled {
			return owners.replace("loki/browser-files", nil, func() { server.RemoveTools(tool.Name) })
		}
		return owners.replace("loki/browser-files", []string{tool.Name}, apply)
	}
	if err := refresh(); err != nil {
		closeAll()
		return nil, err
	}
	watchCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		last := ""
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				current := ""
				for _, option := range options {
					if option.Revision != nil {
						current += option.Revision() + "\n"
					}
				}
				if current != last {
					last = current
					_ = refresh()
				}
			}
		}
	}()
	return func() { cancel(); <-done; closeAll() }, nil
}
