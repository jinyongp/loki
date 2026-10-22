//go:build linux

package application

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"

	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/fault"
	"loki/internal/platform/privateio"
)

var revisionPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type pageCursor struct {
	Version     int    `json:"version"`
	Principal   string `json:"principal"`
	Resource    string `json:"resource"`
	Environment string `json:"env"`
	Action      string `json:"action"`
	Revision    string `json:"revision"`
	Policy      string `json:"policy"`
	Offset      int    `json:"offset"`
}

func metadataRevision(items any) string {
	raw, _ := json.Marshal(items)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *Service) page(principal, resource, env, revision string, items []map[string]any, request appvalues.Request) (map[string]any, error) {
	cursor := pageCursor{Version: 1, Principal: principal, Resource: resource, Environment: env, Action: request.Action, Revision: revision, Policy: s.options.PolicySHA256}
	if request.Cursor != nil {
		raw, err := base64.RawURLEncoding.Strict().DecodeString(*request.Cursor)
		if err != nil || len(raw) > 4096 {
			return nil, invalid()
		}
		var supplied pageCursor
		if privateio.Decode(raw, &supplied) != nil {
			return nil, invalid()
		}
		cursor.Offset = supplied.Offset
		if supplied != cursor || cursor.Offset < 0 || cursor.Offset > len(items) {
			return nil, conflict()
		}
	}
	end := min(cursor.Offset+request.PageLimit(), len(items))
	for {
		var next any
		if end < len(items) {
			following := cursor
			following.Offset = end
			raw, _ := json.Marshal(following)
			next = base64.RawURLEncoding.EncodeToString(raw)
		}
		out := map[string]any{"action": request.Action, "revision": revision, "items": items[cursor.Offset:end], "next_cursor": next}
		if request.Resource != nil {
			out["resource"] = *request.Resource
		}
		if request.Action == "values" {
			out["env"] = request.Env()
		}
		raw, err := json.Marshal(out)
		if err == nil && len(raw) < privateio.MaxControlFrame-1024 {
			return out, nil
		}
		if end-cursor.Offset <= 1 {
			return nil, fault.New(fault.CodeLimitExceeded, "application metadata item exceeds the response bound", false, "ask the administrator to reduce oversized resource metadata")
		}
		end--
	}
}

func (s *Service) resources(principal string, request appvalues.Request) (map[string]any, error) {
	items := []map[string]any{}
	for _, r := range s.sortedResources() {
		for _, grant := range r.Principals {
			if grant.Principal == principal {
				items = append(items, map[string]any{"id": r.ID, "alias": r.Alias, "environments": append([]string(nil), r.Environments...), "metadata": grant.Metadata, "write": grant.Write, "execute": grant.Execute})
				break
			}
		}
	}
	return s.page(principal, "", "", s.options.PolicySHA256, items, request)
}

func (s *Service) status(ctx context.Context, r controlpolicy.ApplicationResource) (map[string]any, error) {
	status, err := s.options.Provider.ProtectionStatus(ctx)
	state := "unavailable"
	verified, pending := false, false
	if err == nil {
		switch status.State {
		case "uninitialized":
			state = "uninitialized"
		case "locked", "locking", "locked_manual":
			state = "locked"
			pending = true
		case "recovery_required":
			state = "recovery_required"
			pending = true
		case "unlocked":
			state = "ready"
			inspection, e := s.options.Provider.InspectInput(ctx, r.Profile(), r.Environments[0], r.Bundle)
			if e != nil {
				state = "unavailable"
				pending = true
			} else {
				pending = inspection.Values.State == "verification_pending"
				if inspection.Values.Current != nil {
					verified = inspection.Values.Current.IntegrityVerified
				}
				if pending {
					state = "locked"
				}
			}
		}
	}
	return map[string]any{"action": "status", "resource": r.Alias, "state": state, "integrity_verified": verified, "verification_pending": pending}, nil
}

func (s *Service) values(ctx context.Context, principal string, r controlpolicy.ApplicationResource, request appvalues.Request) (map[string]any, error) {
	before, err := s.options.Provider.Revision(ctx, r.Profile(), request.Env())
	if err != nil {
		return nil, safeError(err)
	}
	items := []map[string]any{}
	verified := true
	for _, kind := range []string{"variable", "secret"} {
		metadata, err := s.options.Provider.Metadata(ctx, r.Profile(), request.Env(), kind)
		if err != nil {
			return nil, safeError(err)
		}
		verified = verified && metadata.IntegrityVerified
		for _, item := range metadata.Items {
			allowed, ok := r.Keys[item.Name]
			if !ok {
				continue
			}
			if allowed != item.Kind {
				return nil, conflict()
			}
			source := "common"
			if item.Source == "env" {
				source = request.Env()
			} else if item.Source != "common" {
				return nil, unavailable()
			}
			items = append(items, map[string]any{"name": item.Name, "kind": item.Kind, "source": source, "present": true})
		}
	}
	inspection, err := s.options.Provider.InspectInput(ctx, r.Profile(), request.Env(), r.Bundle)
	if err != nil {
		return nil, safeError(err)
	}
	if inspection.Values.Current != nil {
		verified = verified && inspection.Values.Current.IntegrityVerified
	} else {
		verified = false
	}
	after, err := s.options.Provider.Revision(ctx, r.Profile(), request.Env())
	if err != nil {
		return nil, safeError(err)
	}
	if before != after {
		return nil, conflict()
	}
	sort.Slice(items, func(i, j int) bool { return items[i]["name"].(string) < items[j]["name"].(string) })
	out, err := s.page(principal, r.ID, request.Env(), after, items, request)
	if err == nil {
		out["integrity_verified"] = verified
		out["verification_pending"] = inspection.Values.State == "verification_pending"
	}
	return out, err
}

func (s *Service) auditPage(principal string, r controlpolicy.ApplicationResource, request appvalues.Request) (map[string]any, error) {
	records, _, err := s.audit.Snapshot()
	if err != nil {
		return nil, unavailable()
	}
	items := []map[string]any{}
	for _, raw := range records {
		var row map[string]any
		if json.Unmarshal(raw, &row) != nil {
			return nil, unavailable()
		}
		if row["resource_id"] != r.ID {
			continue
		}
		item := map[string]any{}
		for _, name := range []string{"at", "principal", "resource", "request_id", "job_id", "action", "result"} {
			if value, ok := row[name]; ok {
				item[name] = value
			}
		}
		items = append(items, item)
	}
	return s.page(principal, r.ID, "", metadataRevision(items), items, request)
}
