package secretoutput

// LimitError reports counts at the rejection point, never patterns or values.
type LimitError struct {
	Kind            string
	Observed, Limit int
}

func (e *LimitError) Error() string        { return "redaction_pattern_limit" }
func (e *LimitError) Is(target error) bool { return target == ErrLimit }

func (e *LimitError) Valid() bool {
	if e == nil || e.Observed <= e.Limit || uint64(e.Observed) > 1<<53-1 {
		return false
	}
	limits := map[string]int{"patterns": MaxPatterns, "pattern_bytes": MaxPatternBytes, "total_pattern_bytes": MaxTotalPatternBytes, "states": MaxStates, "outputs_per_state": MaxOutputsPerState}
	limit, ok := limits[e.Kind]
	return ok && e.Limit == limit
}
