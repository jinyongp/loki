package main

import (
	"encoding/json"
	"fmt"
	"os"

	"loki/internal/contract"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: gencurrent OUTPUT")
		os.Exit(2)
	}
	baseline, err := contract.Baseline()
	if err != nil {
		panic(err)
	}
	definitions, err := contract.CurrentDefinitions()
	if err != nil {
		panic(err)
	}
	tools := make([]json.RawMessage, 0, len(definitions))
	for _, definition := range definitions {
		data, err := json.Marshal(definition)
		if err != nil {
			panic(err)
		}
		tools = append(tools, data)
	}
	var initialize map[string]any
	if err = json.Unmarshal(baseline.Initialize, &initialize); err != nil {
		panic(err)
	}
	initialize["instructions"] = contract.CurrentInstructions
	initialize["serverInfo"] = map[string]string{"name": "loki", "version": "0.48.0-dev"}
	initializeJSON, err := json.Marshal(initialize)
	if err != nil {
		panic(err)
	}
	current := contract.Snapshot{Baseline: "go-0.48.0-dev", Initialize: initializeJSON, Tools: tools, Resources: baseline.Resources, ResourceTemplates: baseline.ResourceTemplates, ResourceContents: baseline.ResourceContents}
	data, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		panic(err)
	}
	data = append(data, '\n')
	if err = os.WriteFile(os.Args[1], data, 0644); err != nil {
		panic(err)
	}
}
