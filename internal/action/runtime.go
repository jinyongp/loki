package action

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"unicode/utf8"

	"loki/internal/callback"
	"loki/internal/fault"
	"loki/internal/process"
	"loki/internal/secret"
)

type RunRequest struct {
	Profile           string            `json:"profile"`
	Action            string            `json:"action_name"`
	CWD               *string           `json:"cwd"`
	BindLocalCallback bool              `json:"bind_local_callback"`
	PublicEnvironment map[string]string `json:"public_environment"`
	LaunchToken       *string           `json:"launch_token"`
}

type Runtime struct {
	mu         sync.Mutex
	closed     bool
	controller secret.Controller
	layout     Layout
	processes  *process.Manager
	ports      *portRegistry
	launches   map[string]preparedAction
	callback   *callback.Proxy
}

func NewRuntime(controller secret.Controller, layout Layout, limits process.ManagerOptions) (*Runtime, error) {
	if controller.Projects == nil || filepath.Clean(layout.Workspace) != filepath.Clean(controller.Projects.WorkspaceRoot) {
		return nil, errors.New("action workspace does not match the controller")
	}
	limits.RequireRedactor = true
	manager, err := process.NewManager(limits)
	if err != nil {
		return nil, err
	}
	layout.PublicMounts = slices.Clone(layout.PublicMounts)
	runtime := &Runtime{controller: controller, layout: layout, processes: manager, ports: &developmentPorts, launches: make(map[string]preparedAction)}
	port := 41800
	if layout.CallbackPort != nil {
		port = *layout.CallbackPort
	}
	runtime.callback, err = callback.New(port, func(id string) (int, bool) {
		offset := int64(0)
		snapshot, err := manager.Read(id, &offset, 1)
		if err != nil {
			return 0, false
		}
		value, ok := snapshot["port"].(json.Number)
		if !ok {
			return 0, false
		}
		port, err := value.Int64()
		return int(port), err == nil && port > 0 && port <= 65535 && snapshot["status"] == "running" && snapshot["action"] == "api"
	})
	if err != nil {
		manager.Close()
		return nil, err
	}
	return runtime, nil
}

func (r *Runtime) Close() {
	r.mu.Lock()
	r.closed = true
	for token, launch := range r.launches {
		r.ports.release(launch.lease)
		delete(r.launches, token)
	}
	r.mu.Unlock()
	r.callback.Close()
	r.processes.Close()
}

type PrepareRequest struct {
	Profile string  `json:"profile"`
	Action  string  `json:"action_name"`
	CWD     *string `json:"cwd"`
}

func (r *Runtime) Prepare(ctx context.Context, request PrepareRequest) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, errors.New("action runtime is closed")
	}
	plan, err := r.controller.ResolveAction(ctx, request.Profile, request.Action, request.CWD)
	if err != nil {
		return nil, err
	}
	if plan.Policy.DynamicPort == nil {
		return nil, fault.Error("action does not use a dynamic port")
	}
	bindings, err := plan.PreviewBindings()
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	token, lease, err := r.reserve(plan)
	if err != nil {
		return nil, err
	}
	return map[string]any{"launch_token": token, "port": lease.port, "local_url": localURL(lease.port), "expires_in_seconds": int(portHold.Seconds()), "backend_routes": bindings.BackendRoutes, "environment_routes": bindings.EnvironmentRoutes, "environment_suffixes": bindings.EnvironmentSuffixes, "required_environment": bindings.RequiredEnvironment}, nil
}

func (r *Runtime) ClearMaterialization(ctx context.Context, request PrepareRequest) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, errors.New("action runtime is closed")
	}
	plan, err := r.controller.ResolveMaterialization(ctx, request.Profile, request.Action)
	if err != nil {
		return nil, err
	}
	for _, item := range r.processes.List()["processes"].([]map[string]any) {
		if item["name"] == plan.Profile+"/"+plan.Action && item["status"] == "running" {
			return nil, fault.Error("action is still running")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	workspace, err := pinned(r.layout.Workspace, true)
	if err != nil {
		return nil, err
	}
	defer workspace.Close()
	binary, err := pinned(r.layout.Binary, false)
	if err != nil {
		return nil, err
	}
	defer binary.Close()
	m, err := materializationOperation(r.layout, plan, workspace, binary, "clear-explicit")
	if err != nil {
		return nil, err
	}
	defer m.close()
	return map[string]any{"profile": plan.Profile, "action": plan.Action, "cleared": m.cleared}, nil
}

func outcome(result map[string]any) map[string]any {
	switch result["status"] {
	case "running":
		result["outcome"] = "running"
	case "exited":
		result["outcome"] = "failed"
		if result["exit_code"] == 0 {
			result["outcome"] = "succeeded"
		}
	}
	return result
}

func (r *Runtime) Run(ctx context.Context, request RunRequest) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, errors.New("action runtime is closed")
	}
	plan, err := r.controller.ResolveAction(ctx, request.Profile, request.Action, request.CWD)
	if err != nil {
		return nil, err
	}
	if request.BindLocalCallback {
		if !plan.Policy.LocalCallback {
			return nil, fault.Error("action is not approved as a local callback target")
		}
	}
	if key := instanceKey(plan); key != nil {
		if existing := r.processes.FindRunning(*key); existing != nil {
			return r.attachCallback(outcome(existing), request.BindLocalCallback), nil
		}
	}
	var lease *portLease
	parameters := launchParameters{}
	if plan.Policy.DynamicPort != nil {
		if request.LaunchToken == nil {
			lease, err = r.ports.allocate(plan.Policy.DynamicPort.Preferred)
		} else {
			lease, err = r.consume(*request.LaunchToken, plan)
		}
		if err != nil {
			return nil, err
		}
		parameters.port = lease.port
	}
	keepAllocation := false
	defer func() {
		if !keepAllocation {
			r.ports.release(lease)
		}
	}()
	parameters.publicEnvironment, err = plan.PublicEnvironment(request.PublicEnvironment, r.layout.PreviewBaseDomain, request.LaunchToken != nil)
	if err != nil {
		return nil, err
	}
	launch, err := prepare(r.layout, plan, parameters)
	if err != nil {
		return nil, err
	}
	defer launch.Close()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	result, err := launch.Start(r.processes)
	if err != nil {
		return nil, err
	}
	keepAllocation = result["reused"] != true
	return r.attachCallback(outcome(result), request.BindLocalCallback), nil
}

func (r *Runtime) attachCallback(result map[string]any, requested bool) map[string]any {
	if !requested {
		return result
	}
	binding, err := r.callback.Bind(result["session_id"].(string))
	if err != nil {
		binding = map[string]any{"bound": false, "origin": r.callback.Status()["origin"], "error": err.Error()}
	}
	result["local_callback"] = binding
	return result
}
func (r *Runtime) BindCallback(id string) (map[string]any, error) {
	if err := validSession(id); err != nil {
		return nil, err
	}
	return r.callback.Bind(id)
}
func (r *Runtime) CallbackStatus() map[string]any { return r.callback.Status() }

func validSession(id string) error {
	if length := utf8.RuneCountInString(id); length < 8 || length > 128 {
		return fault.Error("invalid process session")
	}
	return nil
}

func (r *Runtime) Read(id string, offset *int64, limit int) (map[string]any, error) {
	if err := validSession(id); err != nil {
		return nil, err
	}
	result, err := r.processes.Read(id, offset, limit)
	if err != nil {
		return nil, err
	}
	return outcome(result), nil
}

func (r *Runtime) List() map[string]any {
	result := r.processes.List()
	for _, item := range result["processes"].([]map[string]any) {
		outcome(item)
	}
	return result
}

func (r *Runtime) Stop(id string) (map[string]any, error) {
	if err := validSession(id); err != nil {
		return nil, err
	}
	result, err := r.processes.Stop(id)
	if err != nil {
		return nil, err
	}
	r.callback.Clear(&id)
	outcome(result)
	if result["status"] == "exited" && result["timed_out"] != true {
		result["outcome"] = "stopped"
	}
	return result, nil
}
