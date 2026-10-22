package remote

import (
	"encoding/json"
	"loki/modules/execution/jobs"
	"testing"
)

func TestLauncherRejectsContradictoryProtectedOutputMetadata(t *testing.T) {
	for _, test := range []struct {
		name     string
		cursor   int64
		complete bool
		gap      *jobs.OutputGap
	}{
		{"negative_cursor", -1, false, nil},
		{"oversized_cursor", jobs.MaxOutputBytes + 1, false, nil},
		{"invented_eof", 4, true, &jobs.OutputGap{Cursor: 4, Reason: "collector_lost", UnknownLength: true}},
		{"wrong_cursor", 4, false, &jobs.OutputGap{Cursor: 5, Reason: "collector_lost", UnknownLength: true}},
		{"unknown_reason", 4, false, &jobs.OutputGap{Cursor: 4, Reason: "invented", UnknownLength: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			id := validWorkload().ID
			socket := fakeLauncherSocket(t, func(map[string]json.RawMessage) []byte {
				return response(t, jobs.OutputSnapshot{JobID: id, State: jobs.StateTerminal, OutputCursor: test.cursor, OutputComplete: test.complete, OutputGap: test.gap})
			})
			launcher, err := New(validOptions(t, socket))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := launcher.Output(t.Context(), id); err == nil {
				t.Fatal("contradictory output metadata accepted")
			}
		})
	}
}
