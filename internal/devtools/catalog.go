// Package devtools defines the pinned CLI contract admitted by Loki.
package devtools

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

const SupportedVersion = "0.9.0"
const ProtocolVersion = 1

//go:embed testdata/catalog-v0.9.0.json
var embeddedCatalog []byte

type Envelope struct {
	SchemaVersion int             `json:"schema_version"`
	OK            bool            `json:"ok"`
	Data          json.RawMessage `json:"data"`
	Error         json.RawMessage `json:"error"`
}

type Catalog struct {
	ProtocolVersion int       `json:"protocol_version"`
	Commands        []Command `json:"commands"`
}

type Command struct {
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	AcceptsChildArgs bool            `json:"accepts_child_args"`
	Options          []Option        `json:"options"`
	InputSchema      json.RawMessage `json:"input_schema"`
	OutputSchema     json.RawMessage `json:"output_schema"`
}

var approvedNames = []string{
	"process restart",
	"process start",
}

func ApprovedNames() []string {
	return append([]string(nil), approvedNames...)
}

func EmbeddedCatalog() ([]Command, error) {
	return ParseCatalog(embeddedCatalog)
}

func ParseCatalog(raw []byte) ([]Command, error) {
	var envelope Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode devtools catalog envelope: %w", err)
	}
	if envelope.SchemaVersion != ProtocolVersion || !envelope.OK || len(envelope.Error) != 0 {
		return nil, errors.New("unsupported devtools catalog envelope")
	}
	var catalog Catalog
	if err := json.Unmarshal(envelope.Data, &catalog); err != nil {
		return nil, fmt.Errorf("decode devtools catalog: %w", err)
	}
	if catalog.ProtocolVersion != ProtocolVersion {
		return nil, fmt.Errorf("unsupported devtools protocol version %d", catalog.ProtocolVersion)
	}

	available := make(map[string]Command, len(catalog.Commands))
	for _, command := range catalog.Commands {
		if command.Name == "" || available[command.Name].Name != "" {
			return nil, errors.New("devtools catalog contains an empty or duplicate command")
		}
		if !json.Valid(command.InputSchema) || !json.Valid(command.OutputSchema) {
			return nil, fmt.Errorf("devtools command %q has an invalid schema", command.Name)
		}
		available[command.Name] = command
	}

	approved := make([]Command, 0, len(approvedNames))
	for _, name := range approvedNames {
		command, ok := available[name]
		if !ok {
			return nil, fmt.Errorf("approved devtools command %q is missing", name)
		}
		if command.AcceptsChildArgs {
			return nil, fmt.Errorf("approved devtools command %q accepts child arguments", name)
		}
		approved = append(approved, command)
	}
	sort.Slice(approved, func(i, j int) bool { return approved[i].Name < approved[j].Name })
	return approved, nil
}
