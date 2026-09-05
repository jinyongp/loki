package process

import (
	"context"
	"regexp"
	"strings"
	"time"
)

var actionScope = regexp.MustCompile(`^loki-action-[0-9a-f]{16}\.scope$`)

func systemdResult(unit string) string {
	if !actionScope.MatchString(unit) {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return collectScopeResult(ctx, unit, func(ctx context.Context, args ...string) (Result, error) {
		return Run(ctx, Spec{Argv: append([]string{"/usr/bin/systemctl"}, args...), CWD: "/", Timeout: 3 * time.Second, MaxOutput: 4096})
	}, 200*time.Millisecond)
}
func collectScopeResult(ctx context.Context, unit string, run func(context.Context, ...string) (Result, error), interval time.Duration) string {
	if !actionScope.MatchString(unit) {
		return ""
	}
	for range 25 {
		result, err := run(ctx, "show", unit, "--property=Result", "--property=ActiveState")
		if err != nil || result.ExitCode != 0 || result.Truncated {
			return ""
		}
		fields := map[string]string{}
		for _, line := range strings.Split(result.Output, "\n") {
			key, value, ok := strings.Cut(line, "=")
			if ok {
				fields[key] = value
			}
		}
		if fields["ActiveState"] == "failed" || fields["ActiveState"] == "inactive" {
			reason := fields["Result"]
			if reason == "" || len(reason) > 128 {
				return ""
			}
			if reason != "success" {
				_, _ = run(ctx, "reset-failed", unit)
			}
			return reason
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ""
		case <-timer.C:
		}
	}
	return ""
}
