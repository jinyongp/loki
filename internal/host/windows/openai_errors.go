package windows

import "strings"

// OpenAIDoctorError keeps a concise cause separate from redacted helper details.
// Callers can show the endpoint and recovery guidance without dumping the report.
type OpenAIDoctorError struct {
	Summary             string
	Endpoint            string
	Details             []string
	LocalEndpointFailed bool
}

func (failure *OpenAIDoctorError) Error() string { return failure.Summary }

func newOpenAIDoctorError(endpoint string, ids, details []string, checks map[string]string) error {
	failure := &OpenAIDoctorError{
		Summary:  "OpenAI tunnel verification failed (" + strings.Join(ids, ", ") + ").",
		Endpoint: endpoint,
		Details:  details,
	}
	for _, id := range ids {
		if id == "mcp_server_reachable" {
			failure.LocalEndpointFailed = true
			failure.Summary = "The local Loki MCP endpoint check failed."
			cause := strings.ToLower(checks[id])
			switch {
			case strings.Contains(cause, "connection refused"), strings.Contains(cause, "actively refused"):
				failure.Summary = "Windows cannot reach the local Loki MCP endpoint (connection refused)."
			case strings.Contains(cause, "timeout"), strings.Contains(cause, "timed out"), strings.Contains(cause, "deadline exceeded"):
				failure.Summary = "Windows timed out connecting to the local Loki MCP endpoint."
			}
			return failure
		}
	}
	for _, id := range ids {
		if id == "oauth_metadata" {
			failure.LocalEndpointFailed = true
			failure.Summary = "The local Loki MCP endpoint's OAuth metadata check failed."
			break
		}
	}
	return failure
}
