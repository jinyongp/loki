//go:build linux

// Package application owns resource-scoped application-value use cases.
// devtools remains the sole value store, cryptographic owner and mutation ledger.
package application

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/audit"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/fault"
	provider "loki/modules/secrets/devtools"
)

type Options struct {
	Provider        *provider.Provider
	Root            string
	Inbox           string
	DeploymentID    string
	PolicySHA256    string
	Resources       []controlpolicy.ApplicationResource
	BaseEnvironment map[string]string
	// Set only by an original host candidate's root-owned broker layout.
	RestoreOperation string
}

type Service struct {
	options Options
	mu      sync.Mutex
	audit   *audit.Log
}

func New(options Options) (*Service, error) {
	if options.Provider == nil || !revisionPattern.MatchString(options.PolicySHA256) || options.DeploymentID == "" || len(options.DeploymentID) > 128 || strings.ContainsAny(options.DeploymentID, "\x00\r\n ") {
		return nil, unavailable()
	}
	if options.RestoreOperation != "" && !adminUUID.MatchString(options.RestoreOperation) {
		return nil, invalid()
	}
	resources, err := controlpolicy.CanonicalApplications(options.Resources, options.BaseEnvironment)
	if err != nil {
		return nil, invalid()
	}
	options.Resources = resources
	base := make(map[string]string, len(options.BaseEnvironment))
	for name, value := range options.BaseEnvironment {
		base[name] = value
	}
	options.BaseEnvironment = base
	if err := privateDirectory(options.Root, false); err != nil {
		return nil, unavailable()
	}
	if err := privateDirectory(options.Inbox, true); err != nil {
		return nil, unavailable()
	}
	if err := cleanupRestoreStaging(options); err != nil {
		return nil, unavailable()
	}
	requests := filepath.Join(options.Root, "requests")
	if err := os.Mkdir(requests, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, unavailable()
	}
	if privateDirectory(requests, false) != nil {
		return nil, unavailable()
	}
	return &Service{options: options, audit: &audit.Log{Path: filepath.Join(options.Root, "audit.jsonl")}}, nil
}

func privateDirectory(path string, inbox bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return invalid()
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	var fs unix.Statfs_t
	if unix.Fstat(fd, &st) != nil || unix.Fstatfs(fd, &fs) != nil || st.Uid != uint32(os.Geteuid()) || st.Mode&07777 != 0700 {
		return invalid()
	}
	if inbox {
		if fs.Type != unix.TMPFS_MAGIC {
			return invalid()
		}
		return nil
	}
	switch fs.Type {
	case unix.EXT4_SUPER_MAGIC, unix.BTRFS_SUPER_MAGIC, unix.XFS_SUPER_MAGIC, unix.OVERLAYFS_SUPER_MAGIC, 0x2fc12fc1:
		return nil
	}
	return invalid()
}

func invalid() error {
	return fault.New(fault.CodeInvalidInput, "invalid application value request", false, "use the resource-scoped operation schema")
}
func unavailable() error {
	return fault.New(fault.CodeUnavailable, "application value service is unavailable", true, "inspect service protection and retry the original request")
}
func denied() error {
	return fault.New(fault.CodeNotAuthorized, "application resource is not authorized", false, "use an administrator-granted resource, environment and key")
}
func conflict() error {
	return fault.New(fault.CodeConflict, "application value metadata changed or conflicts", false, "inspect the current resource revision before a new edit")
}

func safeError(err error) error {
	if err == nil {
		return nil
	}
	var p *provider.Failure
	if !errors.As(err, &p) {
		return unavailable()
	}
	// Unverified routing identifiers explain recovery but authorize no retry.
	if p.Code == "vault_recovery_required" && p.Retry == "same_request" && p.RequestID != "" {
		return fault.New(fault.CodeRecoveryRequired, "application vault protection change requires recovery", false, "ask the host administrator to inspect protection status and resume rewrap with original request "+p.RequestID+" and target passphrase; do not initialize or delete protection")
	}
	switch p.Code {
	case "revision_conflict", "request_conflict", "kind_conflict", "input_changed", "vault_already_initialized":
		return conflict()
	case "key_access_denied":
		return fault.New(fault.CodeDenied, "application vault protection denied access", false, "verify the original passphrase; do not initialize over existing state")
	case "value_exists":
		return fault.New(fault.CodeValueExists, "the selected layer already contains a value", false, "unset the exact layer or use the administrator replacement operation")
	case "request_expired":
		return fault.New(fault.CodeRequestExpired, "application value request has expired", false, "inspect the saved operation history")
	case "vault_locked", "lock_pending", "key_interaction_required", "key_backend_locked":
		return fault.New(fault.CodeVaultLocked, "application vault is locked", true, "ask the host administrator to unlock the existing vault")
	case "vault_recovery_required", "key_missing", "integrity_failed", "key_session_protocol_mismatch":
		return fault.New(fault.CodeRecoveryRequired, "application vault requires recovery", false, "inspect protection; do not initialize over existing state")
	case "vault_input_too_large", "limit_exceeded":
		return fault.New(fault.CodeLimitExceeded, "application value limit exceeded", false, "reduce the bounded input")
	case "redaction_unavailable":
		next := "reduce the selected application keys or bundle while keeping output protection enabled"
		if p.Limit != nil && p.Limit.Valid() {
			next = fmt.Sprintf("%s; %s observed=%d limit=%d", next, p.Limit.Kind, p.Limit.Observed, p.Limit.Limit)
		}
		return fault.New(fault.CodeLimitExceeded, "application output protection limit exceeded", false, next)
	case "delivery_outcome_unknown", "delivery_pending":
		return fault.New(fault.CodeDeliveryUnknown, "application delivery outcome requires inspection", false, "inspect the original Job and delivery record; do not prepare or send another execution")
	case "passphrase_required", "passphrase_mismatch":
		return fault.New(fault.CodeInvalidInput, "application protection passphrase input is required or inconsistent", false, "provide the original operation credentials through private input")
	case "value_expired":
		return fault.New(fault.CodeInputExpired, "the selected application input has expired", false, "inspect the exact layer before an administrator edit")
	case "invalid_argument", "env_not_found", "key_not_found":
		return invalid()
	}
	return unavailable()
}

func (s *Service) resource(principal, name, env, permission string) (controlpolicy.ApplicationResource, error) {
	for _, r := range s.options.Resources {
		if r.Alias == name {
			if env != "" && r.Authorize(principal, r.Repository, env, permission) == nil {
				return r, nil
			}
			if env == "" {
				for _, allowed := range r.Environments {
					if r.Authorize(principal, r.Repository, allowed, permission) == nil {
						return r, nil
					}
				}
			}
		}
	}
	return controlpolicy.ApplicationResource{}, denied()
}

func (s *Service) Handle(ctx context.Context, principal, operation string, request appvalues.Request) (map[string]any, error) {
	if request.Validate(operation) != nil {
		return nil, invalid()
	}
	if operation == "inspect" && request.Action == "resources" {
		return s.resources(principal, request)
	}
	permission := "metadata"
	if operation != "inspect" {
		permission = "write"
	}
	environment := request.Env()
	if request.Action == "status" || request.Action == "audit" {
		environment = ""
	}
	r, err := s.resource(principal, *request.Resource, environment, permission)
	if err != nil {
		return nil, err
	}
	if operation == "inspect" {
		switch request.Action {
		case "status":
			return s.status(ctx, r)
		case "values":
			return s.values(ctx, principal, r, request)
		case "imports":
			return s.imports(principal, r, request)
		case "audit":
			return s.auditPage(principal, r, request)
		}
	}
	if request.Name != nil {
		kind, ok := r.Keys[*request.Name]
		if !ok || controlpolicy.ReservedApplicationKey(*request.Name, s.options.BaseEnvironment) || request.Action == "generate" && kind != "secret" || request.Action == "set_public" && kind != "variable" || request.Kind != nil && kind != *request.Kind {
			return nil, denied()
		}
	}
	if request.Value != nil && strings.ContainsRune(*request.Value, 0) {
		return nil, invalid()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireWritable(); err != nil {
		return nil, err
	}
	if request.Action == "import_staged" {
		return s.importStaged(ctx, principal, r, request)
	}
	created, err := s.ensureLink(principal, r, request)
	if err != nil {
		return nil, err
	}
	if created {
		if _, e := s.options.Provider.Request(ctx, r.Profile(), *request.RequestID); e == nil {
			return nil, conflict()
		} else {
			var failure *provider.Failure
			if !errors.As(e, &failure) || failure.Code != "request_not_found" {
				return nil, safeError(e)
			}
		}
	}
	if request.Action == "unset" {
		return s.unset(ctx, principal, r, request)
	}
	var result provider.ChangeResult
	switch request.Action {
	case "set_public":
		result, err = s.options.Provider.SetPublic(ctx, r.Profile(), request.Env(), *request.Name, *request.Value, *request.ExpectedRevision, *request.RequestID)
	case "generate":
		result, err = s.options.Provider.Generate(ctx, r.Profile(), request.Env(), *request.Name, *request.ExpectedRevision, *request.RequestID)
	default:
		return nil, invalid()
	}
	if err != nil {
		s.record(principal, r, request.Action, *request.RequestID, string(fault.Describe(safeError(err)).Code))
		return nil, safeError(err)
	}
	if !revisionPattern.MatchString(result.Revision) {
		return nil, unavailable()
	}
	response := mutationResponse(request, r, result)
	if request.Action == "generate" {
		response["bytes"] = 32
	}
	resultCode := "success"
	if result.Replayed {
		resultCode = "replayed"
	}
	if err := s.record(principal, r, request.Action, *request.RequestID, resultCode); err != nil {
		return nil, err
	}
	return response, nil
}

func mutationResponse(request appvalues.Request, r controlpolicy.ApplicationResource, result provider.ChangeResult) map[string]any {
	out := map[string]any{"action": request.Action, "resource": r.Alias, "env": request.Env(), "revision": result.Revision, "request_id": *request.RequestID, "changed": result.Changed, "replayed": result.Replayed}
	if request.Name != nil {
		out["name"] = *request.Name
	}
	return out
}

func (s *Service) record(principal string, r controlpolicy.ApplicationResource, action, request, result string) error {
	row := map[string]any{"at": time.Now().UTC().Format(time.RFC3339Nano), "principal": principal, "resource": r.Alias, "resource_id": r.ID, "action": action, "result": result}
	if request != "" {
		row["request_id"] = request
	}
	if err := s.audit.Append(row); err != nil {
		return unavailable()
	}
	return nil
}

func (s *Service) sortedResources() []controlpolicy.ApplicationResource {
	items := append([]controlpolicy.ApplicationResource(nil), s.options.Resources...)
	sort.Slice(items, func(i, j int) bool { return items[i].Alias < items[j].Alias })
	return items
}
