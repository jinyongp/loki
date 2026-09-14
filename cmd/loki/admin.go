package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"loki/internal/admin"
	"loki/internal/fault"
	"loki/internal/rpc"
	"loki/internal/service"
)

func runAdministration(args []string, stdout, stderr io.Writer) int {
	uid := uint32(0)
	return executeAdministration(args, rpc.Client{Socket: "/run/loki-go/runtime/control.sock", ExpectedUID: &uid}, stdout, stderr)
}

func executeAdministration(args []string, client service.RuntimeCaller, stdout, stderr io.Writer) int {
	return executeAdministrationInput(args, client, admin.ReadSecret, stdout, stderr)
}

func executeAdministrationInput(args []string, client service.RuntimeCaller, readSecret func(context.Context, io.Writer) (string, error), stdout, stderr io.Writer) int {
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
	if request["operation"] == "secret_set" {
		request["value"], err = readSecret(ctx, stderr)
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
