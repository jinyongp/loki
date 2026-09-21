package releases

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
)

const GenerationContractVersion = 1

var (
	releaseNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	digestPattern      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	sha256Pattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
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
	}{ContractVersion: GenerationContractVersion, Spec: normalized})
	if err != nil {
		return Generation{}, err
	}
	sum := sha256.Sum256(raw)
	return Generation{ID: "sha256:" + hex.EncodeToString(sum[:]), Spec: normalized}, nil
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
	spec.Rollback.OptionalComponentState = slices.Compact(optional)
	return spec, nil
}
