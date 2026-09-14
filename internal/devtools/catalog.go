// Package devtools defines the pinned CLI contract admitted by Loki.
package devtools

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

const SupportedVersion = "0.8.2"
const ProtocolVersion = 1

//go:embed testdata/catalog-v0.8.2.json
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
	"doctor",
	"env create",
	"env list",
	"env remove",
	"instance list",
	"instance name",
	"instance show",
	"port allocate",
	"port check",
	"port list",
	"port release",
	"port show",
	"process check",
	"process list",
	"process restart",
	"process start",
	"process status",
	"process stop",
	"process wait",
	"project inspect",
	"task add",
	"task cancel",
	"task checkpoint",
	"task checkpoint list",
	"task claim",
	"task context",
	"task current",
	"task depends set",
	"task done",
	"task history",
	"task hold",
	"task impact",
	"task list",
	"task next",
	"task release",
	"task reopen",
	"task resume",
	"task show",
	"task sync",
	"task takeover",
	"task tree",
	"task unclaim",
	"task unhold",
	"task update",
	"task validation accept",
	"task validation add",
	"task validation basis",
	"task validation list",
	"task validation record",
	"task validation show",
	"task validation unwaive",
	"task validation update",
	"task validation waive",
	"task workstream activate",
	"task workstream cancel",
	"task workstream check",
	"task workstream close",
	"task workstream context",
	"task workstream create",
	"task workstream depends set",
	"task workstream edit",
	"task workstream history",
	"task workstream impact",
	"task workstream list",
	"task workstream plan set",
	"task workstream plan show",
	"task workstream reopen",
	"task workstream show",
	"task workstream spec set",
	"task workstream spec show",
	"task workstream tree",
	"task workstream update",
	"variable get",
	"variable list",
	"variable set",
	"variable unset",
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
