// readinessprobe records synthetic observations for the reviewed Go baseline.
//
// It is intentionally non-gating: exit code 0 means every observation completed,
// not that the observed product behavior is correct. As defects are fixed, move
// each scenario into a positive regression test and retire the corresponding
// baseline observation.
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

type report struct {
	R7  r7Observation  `json:"r7_process_cleanup"`
	R14 r14Observation `json:"r14_validation"`
}

func main() {
	observed, err := runAll()
	if err != nil {
		fmt.Fprintln(os.Stderr, "readiness probe could not complete:", err)
		os.Exit(1)
	}
	encoded, err := json.MarshalIndent(observed, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "readiness probe could not encode results:", err)
		os.Exit(1)
	}
	fmt.Println(string(encoded))
}

func runAll() (report, error) {
	var out report
	var err error
	if out.R7, err = observeProcessCleanup(); err != nil {
		return report{}, fmt.Errorf("process observations: %w", err)
	}
	out.R14 = observeValidation()
	return out, nil
}
