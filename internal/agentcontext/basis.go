package agentcontext

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maxContextIdentifierBytes = 4096
	maxContextSkills          = 256
	maxContextRefs            = 200
	maxContextGaps            = 64
)

var (
	contextDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	contextUUIDPattern   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	contextCodePattern   = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
)

type ContextSkillRevision struct {
	Name     string `json:"name"`
	Scope    string `json:"scope"`
	Revision string `json:"revision"`
}

type ContextBasis struct {
	RepositoryID            string                 `json:"repository_id"`
	WorktreeID              string                 `json:"worktree_id"`
	WorkstreamID            string                 `json:"workstream_id,omitempty"`
	TaskID                  string                 `json:"task_id,omitempty"`
	RunID                   string                 `json:"run_id,omitempty"`
	CoordinationFingerprint string                 `json:"coordination_fingerprint,omitempty"`
	CoordinationRevision    string                 `json:"coordination_revision,omitempty"`
	HistoryCursor           string                 `json:"history_cursor,omitempty"`
	CodeBasis               string                 `json:"code_basis,omitempty"`
	GuidanceRevision        string                 `json:"guidance_revision,omitempty"`
	Skills                  []ContextSkillRevision `json:"skills"`
	ValidationRecordIDs     []string               `json:"validation_record_ids"`
	EvidenceRefs            []string               `json:"evidence_refs"`
	Gaps                    []string               `json:"gaps"`
}

func validateContextText(value string, maximum int, field string) error {
	if len(value) > maximum || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return errors.New("invalid context " + field)
	}
	return nil
}

func validateOptionalUUID(value, field string) error {
	if value != "" && !contextUUIDPattern.MatchString(value) {
		return errors.New("invalid context " + field)
	}
	return nil
}

func validateOptionalDigest(value, field string) error {
	if value != "" && !contextDigestPattern.MatchString(value) {
		return errors.New("invalid context " + field)
	}
	return nil
}

func uniqueSorted(values []string, maximum int, field string, validator func(string) error) ([]string, error) {
	if len(values) > maximum {
		return nil, errors.New("too many context " + field)
	}
	out := append([]string{}, values...)
	for _, value := range out {
		if err := validator(value); err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	out = slices.Compact(out)
	return out, nil
}

func normalizeContextBasis(b ContextBasis) (ContextBasis, error) {
	if !contextDigestPattern.MatchString(b.RepositoryID) || !contextDigestPattern.MatchString(b.WorktreeID) {
		return ContextBasis{}, errors.New("context repository and worktree identities must be SHA-256 digests")
	}
	for value, field := range map[string]string{
		b.WorkstreamID: "workstream id",
		b.TaskID:       "task id",
		b.RunID:        "run id",
	} {
		if err := validateOptionalUUID(value, field); err != nil {
			return ContextBasis{}, err
		}
	}
	for value, field := range map[string]string{
		b.CoordinationFingerprint: "coordination fingerprint",
		b.CodeBasis:               "code basis",
		b.GuidanceRevision:        "guidance revision",
	} {
		if err := validateOptionalDigest(value, field); err != nil {
			return ContextBasis{}, err
		}
	}
	if err := validateContextText(b.CoordinationRevision, 1024, "coordination revision"); err != nil {
		return ContextBasis{}, err
	}
	if err := validateContextText(b.HistoryCursor, maxContextIdentifierBytes, "history cursor"); err != nil {
		return ContextBasis{}, err
	}
	if len(b.Skills) > maxContextSkills {
		return ContextBasis{}, errors.New("too many context Skill revisions")
	}
	skills := append([]ContextSkillRevision{}, b.Skills...)
	for _, skill := range skills {
		if !skillNamePattern.MatchString(skill.Name) ||
			(skill.Scope != "project" && skill.Scope != "user" && skill.Scope != "packaged") ||
			!contextDigestPattern.MatchString(skill.Revision) {
			return ContextBasis{}, errors.New("invalid context Skill revision")
		}
	}
	sort.Slice(skills, func(i, j int) bool {
		if skills[i].Name == skills[j].Name {
			if skills[i].Scope == skills[j].Scope {
				return skills[i].Revision < skills[j].Revision
			}
			return skills[i].Scope < skills[j].Scope
		}
		return skills[i].Name < skills[j].Name
	})
	for index := 1; index < len(skills); index++ {
		if skills[index-1].Name == skills[index].Name && skills[index-1].Scope == skills[index].Scope {
			return ContextBasis{}, errors.New("duplicate context Skill revision")
		}
	}
	validation, err := uniqueSorted(b.ValidationRecordIDs, maxContextRefs, "validation records", func(value string) error {
		if !contextUUIDPattern.MatchString(value) {
			return errors.New("invalid context validation record id")
		}
		return nil
	})
	if err != nil {
		return ContextBasis{}, err
	}
	evidence, err := uniqueSorted(b.EvidenceRefs, maxContextRefs, "evidence refs", func(value string) error {
		if value == "" {
			return errors.New("invalid context evidence ref")
		}
		return validateContextText(value, 1024, "evidence ref")
	})
	if err != nil {
		return ContextBasis{}, err
	}
	gaps, err := uniqueSorted(b.Gaps, maxContextGaps, "gaps", func(value string) error {
		if !contextCodePattern.MatchString(value) || len(value) > 128 {
			return errors.New("invalid context gap code")
		}
		return nil
	})
	if err != nil {
		return ContextBasis{}, err
	}
	b.Skills = skills
	b.ValidationRecordIDs = validation
	b.EvidenceRefs = evidence
	b.Gaps = gaps
	return b, nil
}

func ContextBasisFingerprint(b ContextBasis) (string, error) {
	normalized, err := normalizeContextBasis(b)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func contextScopeKey(b ContextBasis) string {
	sum := sha256.Sum256([]byte(b.RepositoryID + "\x00" + b.WorktreeID + "\x00" + b.WorkstreamID))
	return hex.EncodeToString(sum[:])
}
