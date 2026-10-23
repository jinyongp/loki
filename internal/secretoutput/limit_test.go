package secretoutput

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func TestLimitDiagnosticsDescribeNormalizedRejectionCounts(t *testing.T) {
	for _, kind := range []string{"patterns", "pattern_bytes", "total_pattern_bytes", "states", "outputs_per_state"} {
		t.Run(kind, func(t *testing.T) {
			var patterns [][]byte
			switch kind {
			case "pattern_bytes":
				patterns = [][]byte{bytes.Repeat([]byte{'a'}, MaxPatternBytes+1)}
			case "patterns":
				for i := 0; i <= MaxPatterns; i++ {
					patterns = append(patterns, []byte(fmt.Sprintf("key-%d", i)))
				}
			case "total_pattern_bytes":
				for i := 0; i < 65; i++ {
					p := bytes.Repeat([]byte{'a'}, MaxPatternBytes)
					copy(p, []byte(fmt.Sprintf("%02d", i)))
					patterns = append(patterns, p)
				}
			case "states":
				for i := 0; i < 5; i++ {
					patterns = append(patterns, bytes.Repeat([]byte{byte('a' + i)}, MaxPatternBytes))
				}
			case "outputs_per_state":
				for i := 1; i <= MaxOutputsPerState+1; i++ {
					patterns = append(patterns, bytes.Repeat([]byte{'a'}, i))
				}
			}
			_, err := New(patterns)
			var limit *LimitError
			if !errors.Is(err, ErrLimit) || !errors.As(err, &limit) || !limit.Valid() || limit.Kind != kind || limit.Observed <= limit.Limit {
				t.Fatalf("wrong bounded rejection facts: %v", err)
			}
		})
	}
}
