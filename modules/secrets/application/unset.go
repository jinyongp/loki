//go:build linux

package application

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/fault"
	"loki/internal/platform/privateio"
	"loki/internal/platform/safeio"
	provider "loki/modules/secrets/devtools"
)

type unsetReceipt struct {
	Revision string `json:"revision"`
	Changed  bool   `json:"changed"`
	Present  bool   `json:"present"`
}

func (s *Service) unset(ctx context.Context, principal string, r controlpolicy.ApplicationResource, q appvalues.Request) (map[string]any, error) {
	path := filepath.Join(s.options.Root, "requests", *q.RequestID+".unset.json")
	saved, err := s.options.Provider.Request(ctx, r.Profile(), *q.RequestID)
	var receipt unsetReceipt
	replayed := false
	if err == nil {
		if saved.State == "expired" {
			return nil, fault.New(fault.CodeRequestExpired, "application value request has expired", false, "inspect saved operation history")
		}
		if saved.State != "completed" || saved.Result == nil {
			return nil, unavailable()
		}
		data, readErr := readPrivate(path, 4096)
		if readErr != nil || privateio.Decode(data, &receipt) != nil || receipt.Revision != saved.Result.Revision || receipt.Changed != saved.Result.Changed {
			return nil, fault.New(fault.CodeDeliveryUnknown, "the original unset committed without a durable fallback observation", false, "inspect current metadata separately; do not repeat the mutation")
		}
		replayed = true
	} else {
		var failure *provider.Failure
		if !errors.As(err, &failure) || failure.Code != "request_not_found" {
			return nil, safeError(err)
		}
		// Capture fallback metadata before the guarded mutation. The revision
		// check binds this observation to the mutation's exact input state.
		present := false
		if q.Env() != "common" {
			metadata, err := s.options.Provider.Metadata(ctx, r.Profile(), "common", *q.Kind)
			if err != nil {
				return nil, safeError(err)
			}
			for _, item := range metadata.Items {
				if item.Name == *q.Name {
					present = true
				}
			}
		}
		revision, err := s.options.Provider.Revision(ctx, r.Profile(), q.Env())
		if err != nil {
			return nil, safeError(err)
		}
		if revision != *q.ExpectedRevision {
			return nil, conflict()
		}
		result, err := s.options.Provider.Unset(ctx, r.Profile(), q.Env(), *q.Kind, *q.Name, *q.ExpectedRevision, *q.RequestID)
		if err != nil {
			return nil, safeError(err)
		}
		if result.Replayed || !revisionPattern.MatchString(result.Revision) {
			return nil, unavailable()
		}
		receipt = unsetReceipt{Revision: result.Revision, Changed: result.Changed, Present: present}
		data, _ := json.Marshal(receipt)
		if err := safeio.PublishPrivate(path, data, false); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return nil, unavailable()
			}
			existing, e := readPrivate(path, 4096)
			var previous unsetReceipt
			if e != nil || privateio.Decode(existing, &previous) != nil || previous != receipt {
				return nil, unavailable()
			}
		}
	}
	out := mutationResponse(q, r, provider.ChangeResult{Revision: receipt.Revision, Changed: receipt.Changed, Replayed: replayed})
	out["kind"], out["effective_present"], out["source"] = *q.Kind, receipt.Present, nil
	if receipt.Present {
		out["source"] = "common"
	}
	code := "success"
	if replayed {
		code = "replayed"
	}
	if err := s.record(principal, r, q.Action, *q.RequestID, code); err != nil {
		return nil, err
	}
	return out, nil
}
