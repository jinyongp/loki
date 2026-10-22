package contract

// SecretContractRevision is enabled by the dedicated broker integration, never
// by changing a legacy vault handler's advertised input schema in isolation.
const SecretContractRevision = 2

const secretUUIDPattern = "^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"

// SecretV2OutputSchema exposes only safe metadata. It never embeds upstream
// arbitrary objects, errors, filesystem paths or private execution handles.
func SecretV2OutputSchema(tool string) map[string]any {
	str := func(pattern string) map[string]any { return map[string]any{"type": "string", "pattern": pattern} }
	object := func(p map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": p, "required": required}
	}
	alias := str("^[a-z][a-z0-9-]{0,62}$")
	revision := str("^[0-9a-f]{64}$")
	name := str("^[A-Za-z_][A-Za-z0-9_]{0,127}$")
	env := str("^[a-zA-Z][a-zA-Z0-9_-]{0,63}$")
	boolean := map[string]any{"type": "boolean"}
	kind := map[string]any{"type": "string", "enum": []string{"variable", "secret"}}
	clock := map[string]any{"type": "string", "format": "date-time"}
	page := func(action string, item map[string]any, scoped bool) map[string]any {
		p := map[string]any{"action": map[string]any{"const": action}, "revision": revision, "items": map[string]any{"type": "array", "maxItems": 200, "items": item}, "next_cursor": map[string]any{"anyOf": []any{map[string]any{"type": "string", "minLength": 1, "maxLength": 4096}, map[string]any{"type": "null"}}}}
		required := []string{"action", "revision", "items", "next_cursor"}
		if scoped {
			p["resource"] = alias
			required = append(required, "resource")
		}
		return object(p, required...)
	}
	if tool == "secret_inspect" {
		resource := object(map[string]any{"id": str(secretUUIDPattern), "alias": alias, "environments": map[string]any{"type": "array", "items": env}, "metadata": boolean, "write": boolean, "execute": boolean}, "id", "alias", "environments", "metadata", "write", "execute")
		value := object(map[string]any{"name": name, "kind": kind, "source": env, "present": boolean}, "name", "kind", "source", "present")
		stage := object(map[string]any{"stage_id": str("^[0-9a-f]{32}$"), "env": env, "bytes": map[string]any{"type": "integer", "minimum": 1, "maximum": 500000}, "created_at": clock}, "stage_id", "env", "bytes", "created_at")
		audit := object(map[string]any{"at": clock, "principal": alias, "resource": alias, "request_id": str(secretUUIDPattern), "job_id": str("^[0-9a-f]{32}$"), "action": str("^[a-z_]{1,64}$"), "result": str("^[a-z_]{1,64}$")}, "at", "principal", "resource", "action", "result")
		status := object(map[string]any{"action": map[string]any{"const": "status"}, "resource": alias, "state": map[string]any{"type": "string", "enum": []string{"ready", "locked", "uninitialized", "recovery_required", "unavailable"}}, "integrity_verified": boolean, "verification_pending": boolean}, "action", "resource", "state", "integrity_verified", "verification_pending")
		values := page("values", value, true)
		values["properties"].(map[string]any)["env"] = env
		values["required"] = append(values["required"].([]string), "env")
		return map[string]any{"type": "object", "oneOf": []any{page("resources", resource, false), values, page("imports", stage, true), status, page("audit", audit, true)}}
	}
	base := func(action string) map[string]any {
		return object(map[string]any{"action": map[string]any{"const": action}, "resource": alias, "env": env, "revision": revision, "request_id": str(secretUUIDPattern), "changed": boolean, "replayed": boolean}, "action", "resource", "env", "revision", "request_id", "changed", "replayed")
	}
	add := func(s map[string]any, k string, p map[string]any) {
		s["properties"].(map[string]any)[k] = p
		s["required"] = append(s["required"].([]string), k)
	}
	if tool == "secret_write" {
		public, generated, imported := base("set_public"), base("generate"), base("import_staged")
		add(public, "name", name)
		add(generated, "name", name)
		add(generated, "bytes", map[string]any{"const": 32})
		add(imported, "stage_id", str("^[0-9a-f]{32}$"))
		add(imported, "count", map[string]any{"type": "integer", "minimum": 1, "maximum": 5000})
		return map[string]any{"type": "object", "oneOf": []any{public, generated, imported}}
	}
	removed := base("unset")
	add(removed, "name", name)
	add(removed, "kind", kind)
	add(removed, "effective_present", boolean)
	add(removed, "source", map[string]any{"anyOf": []any{env, map[string]any{"type": "null"}}})
	return removed
}

// SecretV2InputSchema is kept separate until the broker and typed transport
// become available together. Unknown and action-irrelevant fields are rejected.
func SecretV2InputSchema(tool string) (map[string]any, error) {
	field := func(pattern, description string) map[string]any {
		return map[string]any{"type": "string", "pattern": pattern, "description": description}
	}
	common := []ActionField{
		{Name: "resource", Schema: field("^[a-z][a-z0-9-]{0,62}$", "Administrator-granted application resource alias; never a vault profile.")},
		{Name: "env", Schema: field("^[a-zA-Z][a-zA-Z0-9_-]{0,63}$", "Granted environment; omission selects common, null is invalid.")},
		{Name: "expected_revision", Schema: field("^[0-9a-f]{64}$", "Opaque revision observed from this application resource.")},
		{Name: "request_id", Schema: field(secretUUIDPattern, "Caller-owned lowercase UUID; retries reuse identical input and UUID.")},
		{Name: "name", Schema: field("^[A-Za-z_][A-Za-z0-9_]{0,127}$", "Exact granted environment key name.")},
		{Name: "stage_id", Schema: field("^[0-9a-f]{32}$", "Private immutable staged import bound to resource, environment, principal and kinds.")},
		{Name: "kind", Schema: map[string]any{"type": "string", "enum": []string{"variable", "secret"}, "description": "Exact value layer to unset; common fallback may remain."}},
		{Name: "value", Schema: map[string]any{"type": "string", "maxLength": 65536, "description": "Explicitly public configuration; credentials must use private staging or generation."}},
		{Name: "cursor", Schema: map[string]any{"type": "string", "minLength": 1, "maxLength": 4096, "description": "Opaque revision-bound metadata page cursor."}},
		{Name: "limit", Schema: map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "default": 50, "description": "Maximum metadata entries in this page."}},
	}
	var variants []ActionVariant
	switch tool {
	case "secret_write":
		variants = []ActionVariant{
			{Name: "set_public", Required: []string{"resource", "name", "value", "expected_revision", "request_id"}, Optional: []string{"env"}},
			{Name: "generate", Required: []string{"resource", "name", "expected_revision", "request_id"}, Optional: []string{"env"}},
			{Name: "import_staged", Required: []string{"resource", "stage_id", "expected_revision", "request_id"}, Optional: []string{"env"}},
		}
	case "secret_delete":
		variants = []ActionVariant{{Name: "unset", Required: []string{"resource", "kind", "name", "expected_revision", "request_id"}, Optional: []string{"env"}}}
	case "secret_inspect":
		variants = []ActionVariant{
			{Name: "resources", Optional: []string{"cursor", "limit"}},
			{Name: "values", Required: []string{"resource"}, Optional: []string{"env", "cursor", "limit"}},
			{Name: "imports", Required: []string{"resource"}, Optional: []string{"env", "cursor", "limit"}},
			{Name: "status", Required: []string{"resource"}},
			{Name: "audit", Required: []string{"resource"}, Optional: []string{"cursor", "limit"}},
		}
	}
	return (ActionInputContract{Title: tool + "Revision2Arguments", ActionDescription: "Resource-scoped application secret operation; values never appear in responses.", Fields: common, Variants: variants}).Schema()
}
