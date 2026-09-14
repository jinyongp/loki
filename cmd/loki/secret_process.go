package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os/signal"
	"syscall"

	"loki/internal/fault"
	"loki/internal/rpc"
	"loki/internal/service"
)

type repeatedStrings []string

func (values *repeatedStrings) String() string { return fmt.Sprint([]string(*values)) }
func (values *repeatedStrings) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func runSecretProcess(args []string, stdout, stderr io.Writer) int {
	uid := uint32(0)
	client := rpc.Client{Socket: "/run/loki-go/runtime/control.sock", ExpectedUID: &uid}
	return executeSecretProcess(args, client, stdout, stderr)
}

func executeSecretProcess(args []string, client service.RuntimeCaller, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "start" && args[0] != "restart") {
		fmt.Fprintln(stderr, "secret-process requires start or restart")
		return 2
	}
	operation := args[0]
	flags := flag.NewFlagSet("secret-process "+operation, flag.ContinueOnError)
	flags.SetOutput(stderr)
	profile := flags.String("profile", "", "Loki encrypted-vault profile")
	requestID := flags.String("request-id", "", "devtools retry-safe request UUID")
	directory := flags.String("dir", ".", "project directory for process start")
	environment := flags.String("env", "", "devtools environment")
	var secrets repeatedStrings
	flags.Var(&secrets, "secret", "encrypted secret name; repeat for multiple values")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if *profile == "" || *requestID == "" || len(secrets) == 0 || flags.NArg() != 1 || operation == "restart" && *directory != "." {
		fmt.Fprintln(stderr, "secret-process requires --profile, --secret, --request-id, and one command or execution ID")
		return 2
	}
	input := map[string]any{"args": []string{flags.Arg(0)}, "request-id": *requestID}
	if operation == "start" {
		input["dir"] = *directory
	}
	if *environment != "" {
		input["env"] = *environment
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		fmt.Fprintln(stderr, "cannot encode secret process request")
		return 1
	}
	request := map[string]any{
		"operation": "devtools_call", "command": "process " + operation,
		"input": json.RawMessage(encoded), "profile": *profile, "secrets": []string(secrets),
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	result, err := client.Call(ctx, request)
	if err != nil {
		fmt.Fprintln(stderr, "loki:", fault.Public(err))
		return 1
	}
	var output any
	decoder := json.NewDecoder(bytes.NewReader(result))
	decoder.UseNumber()
	if decoder.Decode(&output) != nil || output == nil {
		fmt.Fprintln(stderr, "invalid runtime response")
		return 1
	}
	writer := json.NewEncoder(stdout)
	writer.SetIndent("", "  ")
	writer.SetEscapeHTML(false)
	if err = writer.Encode(output); err != nil {
		return 1
	}
	return 0
}
