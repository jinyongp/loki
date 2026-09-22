package mcptransport

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/fault"
	"loki/internal/integrations/sharing/artifacts"
	"loki/internal/integrations/sharing/previews"
	"loki/internal/mcpserver"
	"loki/internal/portguard"
	"loki/internal/rpc"
	"loki/internal/work/jobs"
)

type PreviewController struct {
	Store   *previews.Store
	Runtime rpc.Caller
	Ports   portguard.Policy
	Inspect func(context.Context, int) (map[string]any, error)
	Jobs    jobs.Controller
}
type previewRequest struct {
	Action    string
	Port      *int
	Routes    *map[string]int
	JobID     string `json:"job_id"`
	Endpoint  string `json:"endpoint"`
	RequestID string `json:"request_id"`
	TTL       int    `json:"ttl_seconds"`
}

func (c *PreviewController) listener(ctx context.Context, port int) (map[string]any, error) {
	result, err := InspectWorkspacePort(ctx, c.Ports, c.Inspect, c.Runtime, port)
	if err != nil {
		return nil, err
	}
	if row := portListener(result); row != nil {
		return row, nil
	}
	return nil, fault.Error(fmt.Sprintf("port %d is not a runner-owned workspace development server", port))
}
func (c *PreviewController) PortAllowed(ctx context.Context, port int) bool {
	_, err := c.listener(ctx, port)
	return err == nil
}

func (c *PreviewController) jobRoute(ctx context.Context, jobID, endpoint string) (previews.Route, error) {
	if c.Jobs == nil {
		return previews.Route{}, fault.New(
			fault.CodeUnavailable, "Job endpoint previews are not configured",
			false, "configure the executor peer before publishing a Job endpoint",
		)
	}
	if err := jobs.ValidateJobID(jobID); err != nil || endpoint == "" {
		return previews.Route{}, fault.New(
			fault.CodeInvalidInput, "Job endpoint preview identity is invalid",
			false, "use a job_id from job start and an endpoint name declared for that Job",
		)
	}
	status, err := c.Jobs.Inspect(ctx, jobID)
	if err != nil {
		return previews.Route{}, err
	}
	if status.State != jobs.StateRunning {
		return previews.Route{}, fault.Error("Job endpoint is not active")
	}
	for _, lease := range status.Endpoints {
		if lease.JobID == jobID && lease.Name == endpoint && lease.State == jobs.EndpointLeaseActive &&
			lease.ID != "" && lease.HostPort >= 1024 && lease.HostPort <= 65535 {
			return previews.Route{Prefix: "/", Port: lease.HostPort, JobID: jobID, LeaseID: lease.ID}, nil
		}
	}
	return previews.Route{}, fault.Error("Job endpoint lease is not active")
}

func (c *PreviewController) RouteAllowed(ctx context.Context, route previews.Route) bool {
	if route.JobID == "" && route.LeaseID == "" {
		return c.PortAllowed(ctx, route.Port)
	}
	if c.Jobs == nil || route.JobID == "" || route.LeaseID == "" {
		return false
	}
	status, err := c.Jobs.Inspect(ctx, route.JobID)
	if err != nil || status.State != jobs.StateRunning {
		return false
	}
	for _, lease := range status.Endpoints {
		if lease.JobID == route.JobID && lease.ID == route.LeaseID &&
			lease.HostPort == route.Port && lease.State == jobs.EndpointLeaseActive {
			return true
		}
	}
	return false
}

func rejectLoopbacks(listener map[string]any) error {
	pid, ok := listener["pid"].(float64)
	if !ok || pid <= 0 {
		return nil
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(int(pid)), "environ"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fault.Error("PREVIEW_ENV_UNREADABLE: publish explicit public routes with action=stack")
	}
	for _, entry := range strings.Split(string(data), "\x00") {
		name, value, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, "PUBLIC_") && !strings.HasPrefix(name, "VITE_") && !strings.HasPrefix(name, "NEXT_PUBLIC_") {
			continue
		}
		u, err := url.Parse(value)
		if err != nil {
			continue
		}
		switch u.Hostname() {
		case "127.0.0.1", "localhost", "::1", "0.0.0.0":
			return fault.Error("PREVIEW_LOCAL_URL: publish explicit public routes with action=stack")
		}
	}
	return nil
}
func previewReplayError(err error) error {
	if errors.Is(err, previews.ErrRequestConflict) {
		return fault.New(fault.CodeConflict, "preview request_id was already used for a different publication", false, "generate a new request_id for changed preview inputs")
	}
	if errors.Is(err, previews.ErrCapacityFull) || errors.Is(err, previews.ErrReplayCapacityFull) {
		return fault.New(
			fault.CodeQuotaExceeded,
			"temporary preview capacity is full",
			true,
			"stop an existing preview or wait for preview/request retention to expire before retrying",
		)
	}
	return err
}

func (c *PreviewController) publishJob(ctx context.Context, r previewRequest) (map[string]any, error) {
	route, err := c.jobRoute(ctx, r.JobID, r.Endpoint)
	if err != nil {
		return nil, err
	}
	routes := []previews.Route{route}
	if replayed, ok, err := c.Store.ReplayRoutes(r.RequestID, routes, r.TTL); err != nil {
		return nil, previewReplayError(err)
	} else if ok {
		replayed["request_id"] = strings.ToLower(r.RequestID)
		return replayed, nil
	}
	published, err := c.Store.PublishRoutesReplay(
		r.RequestID, routes, "/workspace", "job:"+r.JobID+"/"+r.Endpoint, r.TTL,
	)
	if err != nil {
		return nil, previewReplayError(err)
	}
	published["request_id"] = strings.ToLower(r.RequestID)
	return published, nil
}

func (c *PreviewController) Publish(ctx context.Context, r previewRequest) (map[string]any, error) {
	if c.Store == nil {
		return nil, fault.Error("temporary live preview sharing is not configured")
	}
	if !previews.ValidRequestID(r.RequestID) {
		return nil, fault.New(fault.CodeInvalidInput, "preview request_id must be a UUID", false, "generate a new UUID request_id")
	}
	if r.TTL == 0 {
		r.TTL = 900
	}
	if r.TTL < 60 || r.TTL > 86400 {
		return nil, fault.New(fault.CodeInvalidInput, "preview lifetime must be between 60 and 86400 seconds", false, "choose ttl_seconds between 60 and 86400")
	}
	if r.Action == "job" {
		return c.publishJob(ctx, r)
	}
	var routes map[string]int
	switch r.Action {
	case "server":
		port, err := mcpserver.Require(r.Port, "port")
		if err != nil {
			return nil, err
		}
		routes = map[string]int{"/": port}
	case "stack":
		var err error
		routes, err = mcpserver.Require(r.Routes, "routes")
		if err != nil {
			return nil, err
		}
	default:
		return nil, fault.New(fault.CodeInvalidInput, "preview_publish action must be server, stack, or job", false, "choose action=server, action=stack, or action=job")
	}
	if replayed, ok, err := c.Store.Replay(r.RequestID, routes, r.TTL); err != nil {
		return nil, previewReplayError(err)
	} else if ok {
		replayed["request_id"] = strings.ToLower(r.RequestID)
		return replayed, nil
	}
	var root map[string]any
	for prefix, port := range routes {
		listener, err := c.listener(ctx, port)
		if err != nil {
			return nil, err
		}
		if prefix == "/" {
			root = listener
		}
	}
	if err := rejectLoopbacks(root); err != nil {
		return nil, err
	}
	cwd, _ := root["cwd"].(string)
	if cwd == "" {
		cwd = "/workspace"
	}
	command, _ := root["command"].(string)
	if command == "" {
		command = "unknown"
	}
	published, err := c.Store.PublishReplay(r.RequestID, routes, cwd, command, r.TTL)
	if err != nil {
		return nil, previewReplayError(err)
	}
	published["request_id"] = strings.ToLower(r.RequestID)
	return published, nil
}

func PreviewHandlers(c *PreviewController, artifactsStore *artifacts.Store) map[string]mcpserver.Handler {
	handlers := map[string]mcpserver.Handler{}
	previewConfigured := c != nil && c.Store != nil
	artifactConfigured := artifactsStore != nil
	if !previewConfigured && !artifactConfigured {
		return handlers
	}
	if previewConfigured {
		handlers["preview_publish"] = mcpserver.Typed(func(ctx context.Context, r previewRequest) (*mcp.CallToolResult, error) {
			return objectResult(c.Publish(ctx, r))
		})
	}
	handlers["shared_resources"] = mcpserver.Typed(func(ctx context.Context, r struct{ Kind string }) (*mcp.CallToolResult, error) {
		previewsResult := map[string]any{"previews": []any{}, "configured": false, "complete": true}
		if previewConfigured {
			previewsResult = map[string]any{"previews": c.Store.List(), "configured": true, "complete": true}
		}
		kind := r.Kind
		if kind == "" {
			kind = "all"
		}
		switch kind {
		case "previews":
			return mcpserver.Object(previewsResult)
		case "artifacts":
			return mcpserver.Object(ArtifactList(artifactsStore))
		case "all":
			return mcpserver.Object(map[string]any{"previews": previewsResult, "artifacts": ArtifactList(artifactsStore)})
		}
		return nil, fault.Error("shared_resources kind must be all, previews, or artifacts")
	})
	handlers["revoke_share"] = mcpserver.Typed(func(ctx context.Context, r struct {
		Kind string
		ID   string `json:"share_id"`
	}) (*mcp.CallToolResult, error) {
		if r.Kind == "artifact" {
			return objectResult(ArtifactRevoke(artifactsStore, r.ID))
		}
		if r.Kind != "preview" {
			return nil, fault.Error("revoke_share kind must be preview or artifact")
		}
		if !previewConfigured {
			return nil, fault.Error("temporary live preview sharing is not configured")
		}
		if !previews.ValidShareID(r.ID) {
			return nil, fault.New(fault.CodeInvalidInput, "preview share_id is invalid", false, "use a share_id returned by preview publication or shared_resources")
		}
		_ = c.Store.Revoke(r.ID)
		return mcpserver.Object(map[string]any{"kind": "preview", "revoked": true, "share_id": r.ID})
	})
	return handlers
}
