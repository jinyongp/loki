package diagnostics

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type Status string

const (
	StatusHealthy  Status = "healthy"
	StatusDegraded Status = "degraded"
	StatusBlocked  Status = "blocked"
)

func (s Status) Valid() bool {
	return s == StatusHealthy || s == StatusDegraded || s == StatusBlocked
}

type Evidence struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Check struct {
	Name     string     `json:"name"`
	Status   Status     `json:"status"`
	Code     string     `json:"code"`
	Summary  string     `json:"summary"`
	Evidence []Evidence `json:"evidence,omitempty"`
}

type Report struct {
	Status      Status  `json:"status"`
	GeneratedAt string  `json:"generated_at"`
	Checks      []Check `json:"checks"`
}

func Healthy(name, code, summary string, evidence ...Evidence) Check {
	return Check{Name: name, Status: StatusHealthy, Code: code, Summary: summary, Evidence: evidence}
}

func Degraded(name, code, summary string, evidence ...Evidence) Check {
	return Check{Name: name, Status: StatusDegraded, Code: code, Summary: summary, Evidence: evidence}
}

func Blocked(name, code, summary string, evidence ...Evidence) Check {
	return Check{Name: name, Status: StatusBlocked, Code: code, Summary: summary, Evidence: evidence}
}

func NewReport(now time.Time, checks ...Check) (Report, error) {
	if now.IsZero() {
		return Report{}, errors.New("diagnostic report timestamp is required")
	}
	if len(checks) == 0 || len(checks) > 32 {
		return Report{}, errors.New("diagnostic report check count is outside the supported range")
	}
	report := Report{
		Status: StatusHealthy, GeneratedAt: now.UTC().Format(time.RFC3339Nano),
		Checks: make([]Check, len(checks)),
	}
	for index, check := range checks {
		if err := validateCheck(check); err != nil {
			return Report{}, err
		}
		report.Checks[index] = cloneCheck(check)
		if statusRank(check.Status) > statusRank(report.Status) {
			report.Status = check.Status
		}
	}
	return report, nil
}

func (r Report) Healthy() bool {
	return r.Status == StatusHealthy
}

func statusRank(status Status) int {
	switch status {
	case StatusHealthy:
		return 0
	case StatusDegraded:
		return 1
	case StatusBlocked:
		return 2
	default:
		return 3
	}
}

func cloneCheck(check Check) Check {
	copy := check
	copy.Evidence = append([]Evidence(nil), check.Evidence...)
	return copy
}

func validateCheck(check Check) error {
	if !safeIdentifier(check.Name, 64) || !check.Status.Valid() || !safeIdentifier(check.Code, 96) {
		return errors.New("diagnostic check identity is invalid")
	}
	if !safePublicText(check.Summary, 256) {
		return errors.New("diagnostic check summary is invalid")
	}
	if len(check.Evidence) > 16 {
		return errors.New("diagnostic check evidence exceeds the supported count")
	}
	seen := map[string]bool{}
	for _, evidence := range check.Evidence {
		if !safeIdentifier(evidence.Name, 64) || sensitiveEvidenceName(evidence.Name) ||
			!safePublicText(evidence.Value, 256) || seen[evidence.Name] {
			return fmt.Errorf("diagnostic evidence %q is invalid or sensitive", evidence.Name)
		}
		seen[evidence.Name] = true
	}
	return nil
}

func safeIdentifier(value string, limit int) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > limit {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func safePublicText(value string, limit int) bool {
	if len(value) == 0 || len(value) > limit {
		return false
	}
	return !strings.ContainsAny(value, "\r\n\x00")
}

func sensitiveEvidenceName(name string) bool {
	name = strings.ToLower(name)
	for _, fragment := range []string{
		"authorization", "cookie", "credential", "password", "private_key", "secret", "token",
	} {
		if strings.Contains(name, fragment) {
			return true
		}
	}
	return false
}
