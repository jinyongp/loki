package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"loki/internal/daemon"
	"loki/internal/execution"
)

type runnerExecPlan struct {
	executable  string
	arguments   []string
	environment []string
}

func buildRunnerExecPlan(args []string) (runnerExecPlan, error) {
	flags := flag.NewFlagSet("runner-exec", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	contractPath := flags.String("contract", "/usr/share/doc/loki/execution-contract.json", "administrator-owned execution contract")
	if err := flags.Parse(args); err != nil {
		return runnerExecPlan{}, err
	}
	command := flags.Args()
	if len(command) == 0 || !filepath.IsAbs(command[0]) {
		return runnerExecPlan{}, errors.New("runner-exec requires an absolute command after --")
	}
	var contract execution.Contract
	if err := daemon.ReadJSON(*contractPath, &contract); err != nil {
		return runnerExecPlan{}, fmt.Errorf("read execution contract: %w", err)
	}
	if err := contract.Validate(); err != nil {
		return runnerExecPlan{}, err
	}
	environment, err := contract.EnvironmentList()
	if err != nil {
		return runnerExecPlan{}, err
	}
	return runnerExecPlan{executable: command[0], arguments: command, environment: environment}, nil
}

func runRunnerExec(args []string, stderr io.Writer) int {
	if os.Geteuid() == 0 {
		fmt.Fprintln(stderr, "runner-exec must run as an unprivileged user")
		return 1
	}
	plan, err := buildRunnerExecPlan(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err = syscall.Exec(plan.executable, plan.arguments, plan.environment); err != nil {
		fmt.Fprintln(stderr, "runner-exec failed:", err)
		return 1
	}
	return 0
}
