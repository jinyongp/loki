package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"loki/internal/management"
)

// Files belong to the frontend. Only protected stdin material crosses hosts;
// the administrator-side manager never opens arbitrary caller-supplied paths.
func remoteIntegrationInput(args []string, input io.Reader) ([]string, io.Reader, func(), error) {
	if len(args) < 3 || args[0] != "integrations" || args[1] != "setup" {
		return args, input, func() {}, nil
	}
	leaf := toolArguments(args[2:])
	if len(leaf) == 0 {
		return args, input, func() {}, nil
	}
	integration := leaf[len(leaf)-1]
	var configPath, keyPath string
	var next []string
	for i := 0; i < len(leaf)-1; i++ {
		arg := leaf[i]
		if arg == "--" {
			continue
		}
		name, value, equal := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if name != "config-file" && name != "private-key-file" && name != "key-file" {
			next = append(next, arg)
			continue
		}
		if !equal {
			i++
			if i >= len(leaf)-1 {
				return nil, nil, nil, fmt.Errorf("missing integration file path")
			}
			value = leaf[i]
		}
		if name == "config-file" {
			if configPath != "" {
				return nil, nil, nil, fmt.Errorf("duplicate configuration input")
			}
			configPath = value
		} else {
			if keyPath != "" {
				return nil, nil, nil, fmt.Errorf("duplicate private key input")
			}
			keyPath = value
		}
	}
	if configPath == "" && keyPath == "" {
		return args, input, func() {}, nil
	}
	for _, arg := range next {
		if arg == "--stdin" || arg == "-stdin" || strings.HasPrefix(arg, "--stdin=") || arg == "--key-stdin" || arg == "-key-stdin" || strings.HasPrefix(arg, "--key-stdin=") {
			return nil, nil, nil, fmt.Errorf("choose file or stdin credential input")
		}
	}
	var encoded []byte
	switch integration {
	case "github":
		imported, err := readGitHubSetup(management.Store{}, configPath, keyPath, false, nil)
		if err != nil {
			return nil, nil, nil, err
		}
		defer clear(imported.PrivateKey)
		encoded, err = json.Marshal(imported)
		if err != nil {
			return nil, nil, nil, err
		}
		next = append(next, "--stdin")
	case "git":
		if configPath != "" || keyPath == "" {
			return nil, nil, nil, fmt.Errorf("Git accepts --key-file for the frontend private key")
		}
		var err error
		encoded, err = integrationFile(keyPath, true)
		if err != nil {
			return nil, nil, nil, err
		}
		next = append(next, "--key-stdin")
	default:
		return nil, nil, nil, fmt.Errorf("unsupported credential import integration")
	}
	return append([]string{"integrations", "setup", integration}, next...), bytes.NewReader(encoded), func() { clear(encoded) }, nil
}
