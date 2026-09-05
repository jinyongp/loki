package main

import (
	"fmt"
	"io"
	"strconv"

	"loki/internal/fault"
	"loki/internal/project"
)

func runMetadataRepair(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "repair-task-metadata requires PROJECTS_ROOT WORKSPACE_GID")
		return 2
	}
	gid, err := strconv.ParseUint(args[1], 10, 32)
	if err != nil {
		fmt.Fprintln(stderr, "invalid workspace GID")
		return 2
	}
	count, err := project.RepairMetadata(args[0], int(gid))
	if err != nil {
		fmt.Fprintln(stderr, fault.Public(err))
		return 1
	}
	fmt.Fprintf(stdout, "repaired_task_metadata=%d\n", count)
	return 0
}
