package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"loki/internal/admin"
	"loki/internal/fault"
	"loki/internal/integrations/github"
	"loki/internal/rpc"
)

func runAdministration(args []string, stdout, stderr io.Writer) int {
	uid := uint32(0)
	return executeAdministration(args, rpc.Client{Socket: "/run/loki-go/runtime/control.sock", ExpectedUID: &uid}, stdout, stderr)
}

func executeAdministration(args []string, client rpc.Caller, stdout, stderr io.Writer) int {
	return executeAdministrationInput(args, client, func(ctx context.Context, prompt io.Writer, multiline bool) (string, error) {
		if multiline {
			return admin.ReadPrivateKey(ctx, prompt)
		}
		return admin.ReadSecret(ctx, prompt)
	}, stdout, stderr)
}

func executeAdministrationInput(args []string, client rpc.Caller, readSecret func(context.Context, io.Writer, bool) (string, error), stdout, stderr io.Writer) int {
	request, err := admin.Request(args)
	if err != nil {
		fmt.Fprintln(stderr, "loki:", fault.Public(err))
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	var source *admin.Dotenv
	deleteSource, _ := request["delete_source"].(bool)
	if file, ok := request["file"].(string); ok {
		if request["operation"] == "stage_env" && os.Geteuid() != 0 {
			fmt.Fprintln(stderr, "staging a dotenv file requires sudo")
			return 1
		}
		source, err = admin.OpenDotenv(file)
		if err != nil {
			fmt.Fprintln(stderr, "loki:", fault.Public(err))
			return 1
		}
		defer source.Close()
		delete(request, "file")
		delete(request, "delete_source")
		request["values"] = source.Values
	}
	if rawValues, ok := request["values_json"].(string); ok {
		var values []githubapp.Value
		decoder := json.NewDecoder(bytes.NewBufferString(rawValues))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&values); err != nil {
			fmt.Fprintln(stderr, "invalid GitHub issue field values")
			return 2
		}
		var trailing any
		if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			fmt.Fprintln(stderr, "invalid GitHub issue field values")
			return 2
		}
		request["values"] = values
		delete(request, "values_json")
	}
	if request["operation"] == "secret_set" || request["operation"] == "github_app_key_set" {
		request["value"], err = readSecret(ctx, stderr, request["operation"] == "github_app_key_set")
		if err != nil {
			fmt.Fprintln(stderr, "secret input failed")
			return 1
		}
	}
	var result json.RawMessage
	if request["operation"] == "stage_env" {
		var staged map[string]any
		staged, err = source.Stage("/var/lib/loki-go/runtime/inbox")
		if err == nil {
			result, err = json.Marshal(staged)
		}
	} else {
		result, err = client.Call(ctx, request)
	}
	delete(request, "value")
	delete(request, "values")
	if err != nil {
		fmt.Fprintln(stderr, "loki:", fault.Public(err))
		return 1
	}
	var decoded map[string]any
	decoder := json.NewDecoder(bytes.NewReader(result))
	decoder.UseNumber()
	if err = decoder.Decode(&decoded); err != nil || decoded == nil {
		fmt.Fprintln(stderr, "invalid runtime response")
		return 1
	}
	if deleteSource {
		if err = source.Remove(); err != nil {
			fmt.Fprintln(stderr, "import succeeded;", fault.Public(err))
			return 1
		}
		decoded["source_deleted"] = true
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err = encoder.Encode(decoded); err != nil {
		return 1
	}
	return 0
}
