package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/signal"
	"syscall"

	"loki/internal/admin"
	"loki/internal/fault"
	"loki/internal/rpc"
	"loki/internal/service"
)

func runAdministration(args []string, stdout, stderr io.Writer) int {
	uid := uint32(0)
	return executeAdministration(args, rpc.Client{Socket: "/run/loki/runtime/control.sock", ExpectedUID: &uid}, stdout, stderr)
}

func executeAdministration(args []string, client service.RuntimeCaller, stdout, stderr io.Writer) int {
	request, err := admin.Request(args)
	if err != nil {
		fmt.Fprintln(stderr, "loki:", fault.Public(err))
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	result, err := client.Call(ctx, request)
	if err != nil {
		fmt.Fprintln(stderr, "loki:", fault.Public(err))
		return 1
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(result))
	decoder.UseNumber()
	if err = decoder.Decode(&decoded); err != nil {
		fmt.Fprintln(stderr, "invalid runtime response")
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err = encoder.Encode(decoded); err != nil {
		return 1
	}
	return 0
}
