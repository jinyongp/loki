package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

const GenerationSchema = 1

const maxDiffValueBytes = 256

type Generation struct {
	canonical []byte
	digest    string
}

type GenerationMetadata struct {
	Schema int    `json:"schema"`
	SHA256 string `json:"sha256"`
}

type Change struct {
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
}

func NewGeneration(document any) (Generation, error) {
	envelope := struct {
		Schema int `json:"schema"`
		Policy any `json:"policy"`
	}{Schema: GenerationSchema, Policy: document}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return Generation{}, fmt.Errorf("encode effective policy: %w", err)
	}
	canonical, err := canonicalJSON(raw)
	if err != nil {
		return Generation{}, err
	}
	sum := sha256.Sum256(canonical)
	return Generation{canonical: canonical, digest: hex.EncodeToString(sum[:])}, nil
}

func canonicalJSON(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode effective policy: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("effective policy contains trailing data")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("canonicalize effective policy: %w", err)
	}
	return canonical, nil
}

func (g Generation) Valid() bool {
	if len(g.canonical) == 0 || len(g.digest) != sha256.Size*2 {
		return false
	}
	sum := sha256.Sum256(g.canonical)
	return g.digest == hex.EncodeToString(sum[:])
}

func (g Generation) Digest() string { return g.digest }

func (g Generation) Metadata() GenerationMetadata {
	if !g.Valid() {
		return GenerationMetadata{}
	}
	return GenerationMetadata{Schema: GenerationSchema, SHA256: g.digest}
}

func (g Generation) CanonicalJSON() []byte { return bytes.Clone(g.canonical) }

func Diff(before, after Generation) ([]Change, error) {
	if !before.Valid() || !after.Valid() {
		return nil, errors.New("effective policy generation is invalid")
	}
	left, err := decodeGeneration(before.canonical)
	if err != nil {
		return nil, err
	}
	right, err := decodeGeneration(after.canonical)
	if err != nil {
		return nil, err
	}
	leftLeaves := map[string]string{}
	rightLeaves := map[string]string{}
	flatten("", left, leftLeaves)
	flatten("", right, rightLeaves)
	paths := make([]string, 0, len(leftLeaves)+len(rightLeaves))
	seen := map[string]struct{}{}
	for path := range leftLeaves {
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	for path := range rightLeaves {
		if _, ok := seen[path]; !ok {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	changes := make([]Change, 0, len(paths))
	for _, path := range paths {
		leftValue, leftOK := leftLeaves[path]
		rightValue, rightOK := rightLeaves[path]
		if leftOK && rightOK && leftValue == rightValue {
			continue
		}
		if !leftOK {
			leftValue = "<missing>"
		}
		if !rightOK {
			rightValue = "<missing>"
		}
		changes = append(changes, Change{Path: path, Before: leftValue, After: rightValue})
	}
	return changes, nil
}

func decodeGeneration(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func flatten(path string, value any, leaves map[string]string) {
	switch value := value.(type) {
	case map[string]any:
		if len(value) == 0 {
			leaves[normalizedPath(path)] = "{}"
			return
		}
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			flatten(path+"/"+escapePointer(key), value[key], leaves)
		}
	case []any:
		if len(value) == 0 {
			leaves[normalizedPath(path)] = "[]"
			return
		}
		for index, item := range value {
			flatten(path+"/"+strconv.Itoa(index), item, leaves)
		}
	default:
		leaves[normalizedPath(path)] = boundedJSON(value)
	}
}

func normalizedPath(path string) string {
	if path == "" {
		return "/"
	}
	return path
}

func escapePointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}

func boundedJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return `"<unrenderable>"`
	}
	if len(raw) <= maxDiffValueBytes {
		return string(raw)
	}
	sum := sha256.Sum256(raw)
	summary, _ := json.Marshal(map[string]any{
		"truncated": true,
		"bytes":     len(raw),
		"sha256":    hex.EncodeToString(sum[:]),
	})
	return string(summary)
}
