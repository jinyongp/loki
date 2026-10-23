// This private acceptance helper is never installed in a shipping image.
// It compares the actual frozen private delivery in memory and emits no values.
package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"loki/internal/app/secretbroker"
	"loki/internal/control/appvalues"
	provider "loki/modules/secrets/devtools"
)

func main() {
	phase := "input"
	if run(&phase) != nil {
		os.Stderr.WriteString("private value comparison failed at " + phase + "\n")
		os.Exit(1)
	}
}

func run(phase *string) error {
	seed := len(os.Args) == 3 && os.Args[2] == "--seed"
	if os.Geteuid() != 10006 || len(os.Args) != 2 && !seed {
		return errors.New("invalid owned fixture")
	}
	body, err := io.ReadAll(io.LimitReader(os.Stdin, 131077))
	defer clear(body)
	if err != nil || len(body) > 131076 {
		return errors.New("invalid private expectation")
	}
	pass, expected, err := appvalues.DecodeApplicationRestore(body)
	if err != nil || len(expected) > 65536 {
		return errors.New("invalid private expectation")
	}
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	*phase = "provider"
	p, err := provider.New(ctx, "/opt/loki/bin/devtools", "/var/lib/loki/secrets/dvt")
	if err != nil {
		return err
	}
	action := "unlock"
	if seed {
		action = "init"
	}
	*phase = "protection"
	if _, err := p.Protection(ctx, action, pass); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = p.Protection(cleanup, "lock", nil)
	}()
	if seed {
		*phase = "seed revision"
		revision, err := p.Revision(ctx, os.Args[1], "common")
		if err != nil {
			return err
		}
		*phase = "seed value"
		if _, err := p.SetSecret(ctx, os.Args[1], "common", "TOKEN", expected, revision, "123e4567-e89b-42d3-a456-426614174489", false); err != nil {
			return err
		}
	}
	const runner = "123e4567-e89b-42d3-a456-426614174481"
	const instance = "123e4567-e89b-42d3-a456-426614174482"
	*phase = "address"
	a, err := p.Address(ctx, runner, instance)
	if err != nil {
		return err
	}
	address := secretbroker.Address{RootID: a.RootID, VaultID: a.VaultID, RunnerID: runner, InstanceID: instance, Endpoint: a.Endpoint, Protocol: a.Protocol, RedactionProtocol: a.RedactionProtocol}
	listener, err := secretbroker.Listen(address)
	if err != nil {
		return err
	}
	defer listener.Close()
	*phase = "prepare"
	e, err := p.Prepare(ctx, os.Args[1], "common", strings.Repeat("f", 32), "123e4567-e89b-42d3-a456-426614174483", "", []string{"TOKEN"})
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	binding := e
	receiver := secretbroker.Receiver{Address: address, Admit: func(_ context.Context, id secretbroker.InputIdentity) error {
		if id.ExecutionID != binding.ID || id.InputID != binding.InputID || id.ExternalExecutionID != binding.JobID {
			return errors.New("wrong private binding")
		}
		return nil
	}, Accept: func(_ context.Context, input *secretbroker.ReceivedInput) error {
		return input.WithValues(func(values, kinds map[string]string) error {
			if len(values) != 1 || kinds["TOKEN"] != "secret" || subtle.ConstantTimeCompare([]byte(values["TOKEN"]), expected) != 1 {
				return errors.New("private value mismatch")
			}
			return nil
		})
	}, Handle: func(context.Context, *secretbroker.ReceivedInput) error { done <- nil; return nil }}
	go func() { _ = receiver.Serve(ctx, listener) }()
	*phase = "register"
	if err := p.Register(ctx, runner, instance, "123e4567-e89b-42d3-a456-426614174484"); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.Unregister(cleanup, runner, instance, "123e4567-e89b-42d3-a456-426614174487")
	}()
	*phase = "approve"
	e, err = p.Approve(ctx, os.Args[1], runner, instance, "123e4567-e89b-42d3-a456-426614174485", e)
	if err != nil {
		return err
	}
	*phase = "deliver"
	e, err = p.Deliver(ctx, os.Args[1], "123e4567-e89b-42d3-a456-426614174486", e)
	if err != nil {
		return err
	}
	select {
	case err := <-done:
		if err != nil {
			return err
		}
		code := int64(0)
		*phase = "report"
		_, err = p.Report(ctx, os.Args[1], "123e4567-e89b-42d3-a456-426614174488", runner, e, provider.EngineFact{State: "finished", ExternalRef: "owned-private-probe", ExitCode: &code})
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
