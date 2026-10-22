// Package appvalues contains the value-free public application-value contract.
// It has no provider, filesystem, process or vault authority.
package appvalues

import (
	"errors"
	"regexp"
)

type Request struct {
	Action           string  `json:"action"`
	Resource         *string `json:"resource,omitempty"`
	Environment      *string `json:"env,omitempty"`
	ExpectedRevision *string `json:"expected_revision,omitempty"`
	RequestID        *string `json:"request_id,omitempty"`
	Name             *string `json:"name,omitempty"`
	Kind             *string `json:"kind,omitempty"`
	Value            *string `json:"value,omitempty"`
	StageID          *string `json:"stage_id,omitempty"`
	Cursor           *string `json:"cursor,omitempty"`
	Limit            *int    `json:"limit,omitempty"`
}

type RPCRequest struct {
	Operation string `json:"operation"`
	Request
}

var alias = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var env = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)
var key = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
var revision = regexp.MustCompile(`^[0-9a-f]{64}$`)
var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var stage = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (r Request) Env() string {
	if r.Environment == nil {
		return "common"
	}
	return *r.Environment
}
func (r Request) PageLimit() int {
	if r.Limit == nil {
		return 50
	}
	return *r.Limit
}

// Validate checks field presence, including irrelevant fields supplied as zero
// values. The transport separately rejects nulls, duplicates and unknown keys.
func (r Request) Validate(operation string) error {
	fields := map[string]bool{"resource": r.Resource != nil, "env": r.Environment != nil, "expected_revision": r.ExpectedRevision != nil, "request_id": r.RequestID != nil, "name": r.Name != nil, "kind": r.Kind != nil, "value": r.Value != nil, "stage_id": r.StageID != nil, "cursor": r.Cursor != nil, "limit": r.Limit != nil}
	var required, optional []string
	switch operation + ":" + r.Action {
	case "inspect:resources":
		optional = []string{"cursor", "limit"}
	case "inspect:values", "inspect:imports":
		required = []string{"resource"}
		optional = []string{"env", "cursor", "limit"}
	case "inspect:status":
		required = []string{"resource"}
	case "inspect:audit":
		required = []string{"resource"}
		optional = []string{"cursor", "limit"}
	case "write:set_public":
		required = []string{"resource", "name", "value", "expected_revision", "request_id"}
		optional = []string{"env"}
	case "write:generate":
		required = []string{"resource", "name", "expected_revision", "request_id"}
		optional = []string{"env"}
	case "write:import_staged":
		required = []string{"resource", "stage_id", "expected_revision", "request_id"}
		optional = []string{"env"}
	case "delete:unset":
		required = []string{"resource", "kind", "name", "expected_revision", "request_id"}
		optional = []string{"env"}
	default:
		return errors.New("unsupported application value action")
	}
	allowed := map[string]bool{}
	for _, name := range required {
		if !fields[name] {
			return errors.New("required application value field is missing")
		}
		allowed[name] = true
	}
	for _, name := range optional {
		allowed[name] = true
	}
	for name, present := range fields {
		if present && !allowed[name] {
			return errors.New("application value field is not allowed for this action")
		}
	}
	for _, item := range []struct {
		value   *string
		pattern *regexp.Regexp
	}{{r.Resource, alias}, {r.Environment, env}, {r.ExpectedRevision, revision}, {r.RequestID, uuid}, {r.Name, key}, {r.StageID, stage}} {
		if item.value != nil && !item.pattern.MatchString(*item.value) {
			return errors.New("invalid application value identifier")
		}
	}
	if r.Kind != nil && *r.Kind != "variable" && *r.Kind != "secret" {
		return errors.New("invalid application value kind")
	}
	if r.Value != nil && len(*r.Value) > 65536 || r.Limit != nil && (*r.Limit < 1 || *r.Limit > 200) || r.Cursor != nil && (len(*r.Cursor) < 1 || len(*r.Cursor) > 4096) {
		return errors.New("application value limit exceeded")
	}
	return nil
}
