// Package devtools defines the CLI contract admitted by Loki.
package devtools

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// ProtocolVersion describes command schemas independently of EnvelopeVersion.
const ProtocolVersion = 3

// This reviewed consumer subset is not a captured full release catalog.
//
//go:embed testdata/catalog-protocol-v3.json
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
	OutputMode       string          `json:"output_mode"`
	Options          []Option        `json:"options"`
	InputSchema      json.RawMessage `json:"input_schema"`
	OutputSchema     json.RawMessage `json:"output_schema"`
}

var approvedNames = []string{
	"command inspect",
	"command list",
	"guidance resolve",
	"process restart",
	"process start",
	"project inspect",
	"skill inspect",
	"skill list",
	"task checkpoint",
	"task checkpoint list",
	"task claim",
	"task context",
	"task current",
	"task done",
	"task history",
	"task next",
	"task release",
	"task resume",
	"task show",
	"task takeover",
	"task workstream context",
	"task workstream history",
	"task workstream list",
	"task workstream show",
}

func ApprovedNames() []string {
	return append([]string(nil), approvedNames...)
}

func EmbeddedCatalog() ([]Command, error) {
	return ParseCatalog(embeddedCatalog)
}

func ParseCatalog(raw []byte) ([]Command, error) {
	data, err := successData(raw)
	if err != nil {
		return nil, err
	}
	var catalog Catalog
	if err := json.Unmarshal(data, &catalog); err != nil {
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
		// Non-JSON commands legitimately have no output schema. Their
		// definitions are not execution authority for this adapter.
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
		if command.OutputMode != "json" {
			return nil, fmt.Errorf("approved devtools command %q requires JSON output mode", name)
		}
		if !jsonObject(command.InputSchema) || !jsonObject(command.OutputSchema) {
			return nil, fmt.Errorf("approved devtools command %q has an invalid schema", name)
		}
		approved = append(approved, command)
	}
	sort.Slice(approved, func(i, j int) bool { return approved[i].Name < approved[j].Name })
	return approved, nil
}
