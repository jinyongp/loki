package lifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	hostingress "loki/internal/host/ingress"
)

const ContractVersion = 1

var (
	releaseNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	digestPattern      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type SchemaRange struct {
	Min uint32 `json:"min"`
	Max uint32 `json:"max"`
}

func (r SchemaRange) Valid() bool {
	return r.Min > 0 && r.Max >= r.Min
}

func (r SchemaRange) Contains(value uint32) bool {
	return r.Valid() && value >= r.Min && value <= r.Max
}

type Compatibility struct {
	Config    SchemaRange `json:"config"`
	Policy    SchemaRange `json:"policy"`
	Toolchain SchemaRange `json:"toolchain"`
	State     SchemaRange `json:"state"`
}

func (c Compatibility) Valid() bool {
	return c.Config.Valid() && c.Policy.Valid() && c.Toolchain.Valid() && c.State.Valid()
}

type Component struct {
	Name     string `json:"name"`
	Digest   string `json:"digest"`
	Optional bool   `json:"optional"`
}

type StateTransition struct {
	From       uint32 `json:"from"`
	To         uint32 `json:"to"`
	Reversible bool   `json:"reversible"`
}

type RollbackCoverage struct {
	StateSnapshot          bool     `json:"state_snapshot"`
	ConfigSnapshot         bool     `json:"config_snapshot"`
	OptionalComponentState []string `json:"optional_component_state,omitempty"`
}

type GenerationSpec struct {
	Version          string            `json:"version"`
	ReleasedAt       time.Time         `json:"released_at"`
	HostBinaryDigest string            `json:"host_binary_digest"`
	CoreImageDigest  string            `json:"core_image_digest"`
	Components       []Component       `json:"components,omitempty"`
	ConfigSchema     uint32            `json:"config_schema"`
	PolicySchema     uint32            `json:"policy_schema"`
	ToolchainSchema  uint32            `json:"toolchain_schema"`
	StateSchema      uint32            `json:"state_schema"`
	Reads            Compatibility     `json:"reads"`
	Migrations       []StateTransition `json:"migrations,omitempty"`
	Rollback         RollbackCoverage  `json:"rollback"`
}

type Generation struct {
	ID   string         `json:"id"`
	Spec GenerationSpec `json:"spec"`
}

func NewGeneration(spec GenerationSpec) (Generation, error) {
	normalized, err := normalizeGenerationSpec(spec)
	if err != nil {
		return Generation{}, err
	}
	raw, err := json.Marshal(struct {
		ContractVersion int            `json:"contract_version"`
		Spec            GenerationSpec `json:"spec"`
	}{ContractVersion: ContractVersion, Spec: normalized})
	if err != nil {
		return Generation{}, err
	}
	sum := sha256.Sum256(raw)
	return Generation{
		ID:   "sha256:" + hex.EncodeToString(sum[:]),
		Spec: normalized,
	}, nil
}

func (g Generation) Valid() bool {
	if !digestPattern.MatchString(g.ID) {
		return false
	}
	expected, err := NewGeneration(g.Spec)
	return err == nil && expected.ID == g.ID
}

func normalizeGenerationSpec(spec GenerationSpec) (GenerationSpec, error) {
	spec.Version = strings.TrimSpace(spec.Version)
	spec.ReleasedAt = spec.ReleasedAt.UTC().Truncate(time.Second)
	if !releaseNamePattern.MatchString(spec.Version) {
		return GenerationSpec{}, errors.New("release version is invalid")
	}
	if spec.ReleasedAt.IsZero() {
		return GenerationSpec{}, errors.New("release timestamp is required")
	}
	if !digestPattern.MatchString(spec.HostBinaryDigest) || !digestPattern.MatchString(spec.CoreImageDigest) {
		return GenerationSpec{}, errors.New("release content digest is invalid")
	}
	if spec.ConfigSchema == 0 || spec.PolicySchema == 0 || spec.ToolchainSchema == 0 || spec.StateSchema == 0 {
		return GenerationSpec{}, errors.New("release schema versions must be positive")
	}
	if !spec.Reads.Valid() {
		return GenerationSpec{}, errors.New("release compatibility ranges are invalid")
	}
	if !spec.Reads.Config.Contains(spec.ConfigSchema) ||
		!spec.Reads.Policy.Contains(spec.PolicySchema) ||
		!spec.Reads.Toolchain.Contains(spec.ToolchainSchema) ||
		!spec.Reads.State.Contains(spec.StateSchema) {
		return GenerationSpec{}, errors.New("release does not declare compatibility with its own schemas")
	}

	components := append([]Component(nil), spec.Components...)
	slices.SortFunc(components, func(left, right Component) int {
		return strings.Compare(left.Name, right.Name)
	})
	for index := range components {
		components[index].Name = strings.TrimSpace(components[index].Name)
		if !releaseNamePattern.MatchString(components[index].Name) || !digestPattern.MatchString(components[index].Digest) {
			return GenerationSpec{}, errors.New("release component is invalid")
		}
		if index > 0 && components[index-1].Name == components[index].Name {
			return GenerationSpec{}, errors.New("release component names must be unique")
		}
	}
	spec.Components = components

	transitions := append([]StateTransition(nil), spec.Migrations...)
	slices.SortFunc(transitions, func(left, right StateTransition) int {
		if left.From != right.From {
			if left.From < right.From {
				return -1
			}
			return 1
		}
		if left.To < right.To {
			return -1
		}
		if left.To > right.To {
			return 1
		}
		return 0
	})
	for index, transition := range transitions {
		if transition.From == 0 || transition.To == 0 || transition.From == transition.To {
			return GenerationSpec{}, errors.New("release state transition is invalid")
		}
		if index > 0 && transitions[index-1].From == transition.From && transitions[index-1].To == transition.To {
			return GenerationSpec{}, errors.New("release state transitions must be unique")
		}
	}
	spec.Migrations = transitions

	optionalComponents := make(map[string]bool, len(spec.Components))
	for _, component := range spec.Components {
		if component.Optional {
			optionalComponents[component.Name] = true
		}
	}
	optional := append([]string(nil), spec.Rollback.OptionalComponentState...)
	for index := range optional {
		optional[index] = strings.TrimSpace(optional[index])
		if !releaseNamePattern.MatchString(optional[index]) || !optionalComponents[optional[index]] {
			return GenerationSpec{}, errors.New("rollback optional-component coverage is invalid")
		}
	}
	slices.Sort(optional)
	optional = slices.Compact(optional)
	spec.Rollback.OptionalComponentState = optional
	return spec, nil
}

type HostState struct {
	ActiveGenerationID string   `json:"active_generation_id,omitempty"`
	ConfigSchema       uint32   `json:"config_schema"`
	PolicySchema       uint32   `json:"policy_schema"`
	ToolchainSchema    uint32   `json:"toolchain_schema"`
	StateSchema        uint32   `json:"state_schema"`
	EnabledComponents  []string `json:"enabled_components,omitempty"`
	IngressHosts       []string `json:"ingress_hosts,omitempty"`
	Revision           string   `json:"revision"`
}

func NormalizeIngressHosts(hosts []string) ([]string, error) {
	return hostingress.Normalize(hosts)
}

func (s HostState) normalized() (HostState, error) {
	if s.ConfigSchema == 0 || s.PolicySchema == 0 || s.ToolchainSchema == 0 || s.StateSchema == 0 {
		return HostState{}, errors.New("host schema versions must be positive")
	}
	s.Revision = strings.TrimSpace(s.Revision)
	if s.Revision == "" || len(s.Revision) > 128 || strings.ContainsAny(s.Revision, "\r\n\x00") {
		return HostState{}, errors.New("host state revision is invalid")
	}
	if s.ActiveGenerationID != "" && !digestPattern.MatchString(s.ActiveGenerationID) {
		return HostState{}, errors.New("active release generation id is invalid")
	}
	components := append([]string(nil), s.EnabledComponents...)
	for index := range components {
		components[index] = strings.TrimSpace(components[index])
		if !releaseNamePattern.MatchString(components[index]) {
			return HostState{}, errors.New("enabled component name is invalid")
		}
	}
	slices.Sort(components)
	s.EnabledComponents = slices.Compact(components)
	hosts, err := NormalizeIngressHosts(s.IngressHosts)
	if err != nil {
		return HostState{}, err
	}
	s.IngressHosts = hosts
	return s, nil
}

type MigrationStep struct {
	From       uint32 `json:"from"`
	To         uint32 `json:"to"`
	Reversible bool   `json:"reversible"`
}

type Impact struct {
	RestartRequired      bool            `json:"restart_required"`
	MigrationRequired    bool            `json:"migration_required"`
	Migration            []MigrationStep `json:"migration,omitempty"`
	OptionalComponents   []string        `json:"optional_components,omitempty"`
	RollbackCompatible   bool            `json:"rollback_compatible"`
	RollbackUsesSnapshot bool            `json:"rollback_uses_snapshot"`
}

type PreparedPlan struct {
	ID                    string    `json:"id"`
	ObservedHostRevision  string    `json:"observed_host_revision"`
	ActiveGenerationID    string    `json:"active_generation_id,omitempty"`
	CandidateGenerationID string    `json:"candidate_generation_id"`
	Impact                Impact    `json:"impact"`
	PreparedAt            time.Time `json:"prepared_at"`
}

func (p PreparedPlan) Valid() bool {
	if !digestPattern.MatchString(p.ID) || p.PreparedAt.IsZero() ||
		p.ObservedHostRevision == "" || !digestPattern.MatchString(p.CandidateGenerationID) {
		return false
	}
	if p.ActiveGenerationID != "" && !digestPattern.MatchString(p.ActiveGenerationID) {
		return false
	}
	expected, err := preparedPlanID(p)
	return err == nil && expected == p.ID
}

func preparedPlanID(plan PreparedPlan) (string, error) {
	raw, err := json.Marshal(struct {
		Version               int    `json:"version"`
		ObservedHostRevision  string `json:"observed_host_revision"`
		ActiveGenerationID    string `json:"active_generation_id,omitempty"`
		CandidateGenerationID string `json:"candidate_generation_id"`
		Impact                Impact `json:"impact"`
	}{
		Version:               ContractVersion,
		ObservedHostRevision:  plan.ObservedHostRevision,
		ActiveGenerationID:    plan.ActiveGenerationID,
		CandidateGenerationID: plan.CandidateGenerationID,
		Impact:                plan.Impact,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func Prepare(active *Generation, candidate Generation, host HostState, now time.Time) (PreparedPlan, error) {
	if !candidate.Valid() {
		return PreparedPlan{}, errors.New("candidate release generation is invalid")
	}
	if active != nil && !active.Valid() {
		return PreparedPlan{}, errors.New("active release generation is invalid")
	}
	normalizedHost, err := host.normalized()
	if err != nil {
		return PreparedPlan{}, err
	}
	if active == nil {
		if normalizedHost.ActiveGenerationID != "" {
			return PreparedPlan{}, errors.New("host records an active release but no active generation was supplied")
		}
	} else if normalizedHost.ActiveGenerationID != active.ID {
		return PreparedPlan{}, errors.New("active release generation does not match host state")
	}
	now = now.UTC().Truncate(time.Second)
	if now.IsZero() {
		return PreparedPlan{}, errors.New("prepare timestamp is required")
	}
	if now.Before(candidate.Spec.ReleasedAt) {
		return PreparedPlan{}, errors.New("candidate release timestamp is in the future")
	}

	if !candidate.Spec.Reads.Config.Contains(normalizedHost.ConfigSchema) {
		return PreparedPlan{}, fmt.Errorf("candidate cannot read config schema %d", normalizedHost.ConfigSchema)
	}
	if !candidate.Spec.Reads.Policy.Contains(normalizedHost.PolicySchema) {
		return PreparedPlan{}, fmt.Errorf("candidate cannot read policy schema %d", normalizedHost.PolicySchema)
	}
	if !candidate.Spec.Reads.Toolchain.Contains(normalizedHost.ToolchainSchema) {
		return PreparedPlan{}, fmt.Errorf("candidate cannot read toolchain schema %d", normalizedHost.ToolchainSchema)
	}
	migration, err := migrationPath(normalizedHost.StateSchema, candidate.Spec.StateSchema, candidate.Spec)
	if err != nil {
		return PreparedPlan{}, err
	}
	if len(migration) == 0 && !candidate.Spec.Reads.State.Contains(normalizedHost.StateSchema) {
		return PreparedPlan{}, fmt.Errorf("candidate cannot read state schema %d", normalizedHost.StateSchema)
	}

	enabled, err := validateEnabledComponents(normalizedHost.EnabledComponents, candidate.Spec.Components)
	if err != nil {
		return PreparedPlan{}, err
	}
	impact := Impact{
		RestartRequired:    active == nil || active.ID != candidate.ID,
		MigrationRequired:  len(migration) != 0,
		Migration:          migration,
		OptionalComponents: enabled,
	}
	impact.RollbackCompatible, impact.RollbackUsesSnapshot = rollbackCompatibility(active, candidate, normalizedHost, migration)

	if active != nil && len(migration) != 0 && !impact.RollbackCompatible {
		return PreparedPlan{}, errors.New("candidate migration has no safe rollback coverage")
	}
	if len(enabled) > 0 && !coversOptionalState(candidate.Spec.Rollback.OptionalComponentState, enabled) {
		if active != nil && active.ID != candidate.ID {
			return PreparedPlan{}, errors.New("candidate rollback coverage omits enabled optional-component state")
		}
	}

	plan := PreparedPlan{
		ObservedHostRevision:  normalizedHost.Revision,
		ActiveGenerationID:    normalizedHost.ActiveGenerationID,
		CandidateGenerationID: candidate.ID,
		Impact:                impact,
		PreparedAt:            now,
	}
	plan.ID, err = preparedPlanID(plan)
	if err != nil {
		return PreparedPlan{}, err
	}
	return plan, nil
}

func migrationPath(from, target uint32, spec GenerationSpec) ([]MigrationStep, error) {
	if from == target {
		return nil, nil
	}
	current := from
	result := make([]MigrationStep, 0, len(spec.Migrations))
	visited := map[uint32]bool{from: true}
	for current != target {
		var selected *StateTransition
		for index := range spec.Migrations {
			transition := &spec.Migrations[index]
			if transition.From != current {
				continue
			}
			if selected != nil {
				return nil, fmt.Errorf("state migration from schema %d is ambiguous", current)
			}
			selected = transition
		}
		if selected == nil {
			return nil, fmt.Errorf("candidate has no migration path from state schema %d to %d", from, target)
		}
		if visited[selected.To] {
			return nil, errors.New("candidate state migration path contains a cycle")
		}
		visited[selected.To] = true
		result = append(result, MigrationStep{From: selected.From, To: selected.To, Reversible: selected.Reversible})
		current = selected.To
	}
	return result, nil
}

func validateEnabledComponents(enabled []string, components []Component) ([]string, error) {
	known := make(map[string]bool, len(components))
	for _, component := range components {
		known[component.Name] = true
	}
	for _, name := range enabled {
		if !known[name] {
			return nil, fmt.Errorf("candidate does not define enabled component %q", name)
		}
	}
	return append([]string(nil), enabled...), nil
}

func rollbackCompatibility(active *Generation, candidate Generation, host HostState, migration []MigrationStep) (bool, bool) {
	if active == nil {
		return false, false
	}
	if active.ID == candidate.ID {
		return true, false
	}
	targetState := candidate.Spec.StateSchema
	if active.Spec.Reads.Config.Contains(candidate.Spec.ConfigSchema) &&
		active.Spec.Reads.Policy.Contains(candidate.Spec.PolicySchema) &&
		active.Spec.Reads.Toolchain.Contains(candidate.Spec.ToolchainSchema) &&
		active.Spec.Reads.State.Contains(targetState) {
		return true, false
	}
	if len(migration) == 0 {
		return false, false
	}
	allReversible := true
	for _, step := range migration {
		if !step.Reversible {
			allReversible = false
			break
		}
	}
	if allReversible {
		return true, false
	}
	if candidate.Spec.Rollback.StateSnapshot && candidate.Spec.Rollback.ConfigSnapshot {
		return true, true
	}
	_ = host
	return false, false
}

func coversOptionalState(coverage, enabled []string) bool {
	covered := make(map[string]bool, len(coverage))
	for _, name := range coverage {
		covered[name] = true
	}
	for _, name := range enabled {
		if !covered[name] {
			return false
		}
	}
	return true
}

type UpdateStatus struct {
	Installed       *Generation   `json:"installed,omitempty"`
	Available       *Generation   `json:"available,omitempty"`
	Prepared        *PreparedPlan `json:"prepared,omitempty"`
	Host            HostState     `json:"host"`
	UpdateAvailable bool          `json:"update_available"`
}

func Status(installed, available *Generation, prepared *PreparedPlan, host HostState) (UpdateStatus, error) {
	normalizedHost, err := host.normalized()
	if err != nil {
		return UpdateStatus{}, err
	}
	if installed != nil {
		if !installed.Valid() || normalizedHost.ActiveGenerationID != installed.ID {
			return UpdateStatus{}, errors.New("installed generation does not match host state")
		}
	} else if normalizedHost.ActiveGenerationID != "" {
		return UpdateStatus{}, errors.New("host records an installed generation that was not supplied")
	}
	if available != nil && !available.Valid() {
		return UpdateStatus{}, errors.New("available generation is invalid")
	}
	if prepared != nil {
		if !prepared.Valid() ||
			prepared.ObservedHostRevision != normalizedHost.Revision ||
			prepared.ActiveGenerationID != normalizedHost.ActiveGenerationID {
			return UpdateStatus{}, errors.New("prepared plan is stale or invalid for current host state")
		}
		if available == nil || prepared.CandidateGenerationID != available.ID {
			return UpdateStatus{}, errors.New("prepared plan does not match available generation")
		}
	}
	return UpdateStatus{
		Installed:       installed,
		Available:       available,
		Prepared:        prepared,
		Host:            normalizedHost,
		UpdateAvailable: available != nil && (installed == nil || available.ID != installed.ID),
	}, nil
}
