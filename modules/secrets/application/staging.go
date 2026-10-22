//go:build linux

package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/fault"
	"loki/internal/platform/privateio"
	"loki/internal/platform/safeio"
	provider "loki/modules/secrets/devtools"
)

const MaxStageBytes = 500000

type StageRequest struct {
	Resource    string   `json:"resource"`
	Environment string   `json:"env"`
	Principal   string   `json:"principal"`
	Variables   []string `json:"variables"`
}

type stagedInput struct {
	Version     int                   `json:"version"`
	ID          string                `json:"stage_id"`
	ResourceID  string                `json:"resource_id"`
	Environment string                `json:"env"`
	Principal   string                `json:"principal"`
	Policy      string                `json:"policy"`
	CreatedAt   string                `json:"created_at"`
	Bytes       int                   `json:"bytes"`
	Kinds       []provider.ChangeItem `json:"kinds"`
}

type requestLink struct {
	Version     int    `json:"version"`
	Deployment  string `json:"deployment"`
	Policy      string `json:"policy"`
	RequestID   string `json:"request_id"`
	Principal   string `json:"principal"`
	ResourceID  string `json:"resource_id"`
	Environment string `json:"env"`
	Action      string `json:"action"`
	Name        string `json:"name,omitempty"`
	Kind        string `json:"kind,omitempty"`
	StageID     string `json:"stage_id,omitempty"`
	Revision    string `json:"revision"`
	CreatedAt   string `json:"created_at"`
}

func stageExpired() error {
	return fault.New(fault.CodeStageExpired, "private staged input is no longer available", false, "stage the original input again using a new request UUID")
}

func readPrivate(path string, maximum int) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "private-application-metadata")
	defer f.Close()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		return nil, unavailable()
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(maximum)+1))
	if err != nil || len(data) > maximum {
		clear(data)
		return nil, unavailable()
	}
	return data, nil
}

func (s *Service) ensureLink(principal string, r controlpolicy.ApplicationResource, q appvalues.Request) (bool, error) {
	link := requestLink{Version: 1, Deployment: s.options.DeploymentID, Policy: s.options.PolicySHA256, RequestID: *q.RequestID, Principal: principal, ResourceID: r.ID, Environment: q.Env(), Action: q.Action, Revision: *q.ExpectedRevision}
	if q.Name != nil {
		link.Name = *q.Name
	}
	if q.Kind != nil {
		link.Kind = *q.Kind
	}
	if q.StageID != nil {
		link.StageID = *q.StageID
	}
	path := filepath.Join(s.options.Root, "requests", link.RequestID+".json")
	data, err := readPrivate(path, 4096)
	if err == nil {
		var previous requestLink
		if privateio.Decode(data, &previous) != nil {
			return false, unavailable()
		}
		previous.CreatedAt = ""
		if previous != link {
			return false, conflict()
		}
		return false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, unavailable()
	}
	link.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	data, _ = json.Marshal(link)
	if err := safeio.PublishPrivate(path, data, false); err != nil {
		return false, unavailable()
	}
	return true, nil
}

// Stage is an administrator-only operation. The raw body arrives on the private
// admin channel; only its immutable ID and metadata can reach public callers.
func (s *Service) Stage(ctx context.Context, q StageRequest, body []byte) (map[string]any, error) {
	if len(body) == 0 || len(body) > MaxStageBytes {
		return nil, invalid()
	}
	if q.Environment == "" {
		q.Environment = "common"
	}
	r, err := s.resource(q.Principal, q.Resource, q.Environment, "write")
	if err != nil {
		return nil, err
	}
	variables := append([]string(nil), q.Variables...)
	sort.Strings(variables)
	for i, name := range variables {
		if r.Keys[name] != "variable" || i > 0 && variables[i-1] == name {
			return nil, denied()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireWritable(); err != nil {
		return nil, err
	}
	if privateDirectory(s.options.Inbox, true) != nil {
		return nil, unavailable()
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, unavailable()
	}
	id := hex.EncodeToString(nonce[:])
	path := filepath.Join(s.options.Inbox, id+".data")
	if err := safeio.PublishPrivate(path, body, false); err != nil {
		return nil, unavailable()
	}
	accepted := false
	defer func() {
		if !accepted {
			os.Remove(path)
		}
	}()
	revision, err := s.options.Provider.Revision(ctx, r.Profile(), q.Environment)
	if err != nil {
		return nil, safeError(err)
	}
	preview, err := s.options.Provider.Import(ctx, r.Profile(), q.Environment, path, revision, privateio.OperationID(s.options.DeploymentID, id, "stage-preview"), r.Bundle, variables, true)
	if err != nil {
		return nil, safeError(err)
	}
	if !preview.Applicable || len(preview.Items) == 0 || len(preview.Items) > 5000 || len(preview.Items) > len(r.Keys) {
		return nil, conflict()
	}
	seen := map[string]bool{}
	for _, item := range preview.Items {
		if seen[item.Name] || r.Keys[item.Name] != item.Kind || controlpolicy.ReservedApplicationKey(item.Name, s.options.BaseEnvironment) {
			return nil, denied()
		}
		seen[item.Name] = true
	}
	stage := stagedInput{Version: 1, ID: id, ResourceID: r.ID, Environment: q.Environment, Principal: q.Principal, Policy: s.options.PolicySHA256, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Bytes: len(body), Kinds: preview.Items}
	encoded, _ := json.Marshal(stage)
	if err := safeio.PublishPrivate(filepath.Join(s.options.Inbox, id+".json"), encoded, false); err != nil {
		return nil, unavailable()
	}
	accepted = true
	if err := s.record("host-admin", r, "stage", "", "success"); err != nil {
		return nil, err
	}
	return stage.metadata(), nil
}

func (stage stagedInput) metadata() map[string]any {
	return map[string]any{"stage_id": stage.ID, "env": stage.Environment, "bytes": stage.Bytes, "created_at": stage.CreatedAt}
}

func (s *Service) loadStage(id string) (stagedInput, error) {
	data, err := readPrivate(filepath.Join(s.options.Inbox, id+".json"), privateio.MaxControlFrame)
	if errors.Is(err, os.ErrNotExist) {
		return stagedInput{}, stageExpired()
	}
	if err != nil {
		return stagedInput{}, unavailable()
	}
	var stage stagedInput
	if privateio.Decode(data, &stage) != nil || stage.Version != 1 || stage.ID != id || stage.Bytes < 1 || stage.Bytes > MaxStageBytes || stage.Policy != s.options.PolicySHA256 {
		return stagedInput{}, stageExpired()
	}
	return stage, nil
}

func (s *Service) discardStage(id string) error {
	for _, suffix := range []string{".data", ".json"} {
		if err := os.Remove(filepath.Join(s.options.Inbox, id+suffix)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return unavailable()
		}
	}
	return nil
}

func (s *Service) importStaged(ctx context.Context, principal string, r controlpolicy.ApplicationResource, q appvalues.Request) (map[string]any, error) {
	created, err := s.ensureLink(principal, r, q)
	if err != nil {
		return nil, err
	}
	saved, receiptErr := s.options.Provider.Request(ctx, r.Profile(), *q.RequestID)
	var result provider.ChangeResult
	if receiptErr == nil {
		if created {
			return nil, conflict()
		}
		if saved.State == "expired" {
			return nil, fault.New(fault.CodeRequestExpired, "application value request has expired", false, "inspect saved operation history")
		}
		if saved.State != "completed" || saved.Result == nil {
			return nil, unavailable()
		}
		result = *saved.Result
		result.Replayed = true
	} else {
		var failure *provider.Failure
		if !errors.As(receiptErr, &failure) || failure.Code != "request_not_found" {
			return nil, safeError(receiptErr)
		}
		stage, err := s.loadStage(*q.StageID)
		if err != nil {
			return nil, err
		}
		if stage.ResourceID != r.ID || stage.Environment != q.Env() || stage.Principal != principal {
			return nil, denied()
		}
		body, err := readPrivate(filepath.Join(s.options.Inbox, stage.ID+".data"), MaxStageBytes)
		if errors.Is(err, os.ErrNotExist) {
			return nil, stageExpired()
		}
		if err != nil {
			return nil, unavailable()
		}
		length := len(body)
		clear(body)
		if length != stage.Bytes {
			return nil, stageExpired()
		}
		variables := []string{}
		for _, item := range stage.Kinds {
			if r.Keys[item.Name] != item.Kind {
				return nil, conflict()
			}
			if item.Kind == "variable" {
				variables = append(variables, item.Name)
			}
		}
		result, err = s.options.Provider.Import(ctx, r.Profile(), q.Env(), filepath.Join(s.options.Inbox, stage.ID+".data"), *q.ExpectedRevision, *q.RequestID, r.Bundle, variables, false)
		if err != nil {
			return nil, safeError(err)
		}
		if !result.Applicable {
			return nil, conflict()
		}
	}
	if !revisionPattern.MatchString(result.Revision) {
		return nil, unavailable()
	}
	if err := s.discardStage(*q.StageID); err != nil {
		return nil, err
	}
	out := mutationResponse(q, r, result)
	out["stage_id"] = *q.StageID
	out["count"] = len(result.Items)
	code := "success"
	if result.Replayed {
		code = "replayed"
	}
	if err := s.record(principal, r, q.Action, *q.RequestID, code); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) imports(principal string, r controlpolicy.ApplicationResource, q appvalues.Request) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.options.Inbox)
	if err != nil {
		return nil, unavailable()
	}
	items := []map[string]any{}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := entry.Name()[:len(entry.Name())-5]
		if len(id) != 32 {
			continue
		}
		if _, err := hex.DecodeString(id); err != nil {
			continue
		}
		stage, err := s.loadStage(id)
		if err != nil {
			continue
		}
		if stage.Principal == principal && stage.ResourceID == r.ID && stage.Environment == q.Env() {
			items = append(items, stage.metadata())
		}
	}
	return s.page(principal, r.ID, q.Env(), metadataRevision(items), items, q)
}
