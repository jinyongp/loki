package main

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/mod/semver"

	windowshost "loki/internal/host/windows"
	"loki/internal/progress"
)

// v0.1.31 makes Loki's stateful MCP endpoint explicitly reject modern
// server/discover so OpenAI clients fall back to the session-bound handshake.
// Frontend-only releases do not change this server requirement.
const openAIMinimumServerRelease = "v0.1.31"

func connectionSetupCompatibilityError(frontendRelease, applianceVersion string, updatePrepared bool) error {
	frontend := normalizedReleaseTag(frontendRelease)
	appliance := normalizedReleaseTag(applianceVersion)
	if !semver.IsValid(frontend) || !semver.IsValid(appliance) {
		return errors.New("cannot verify Loki frontend/appliance release compatibility; run 'loki update status' and retry")
	}
	if semver.Compare(appliance, openAIMinimumServerRelease) >= 0 {
		return nil
	}
	message := fmt.Sprintf("OpenAI connection setup requires Loki server %s or later; running server is %s.\n", openAIMinimumServerRelease, appliance)
	if updatePrepared {
		return errors.New(message + "An appliance update is already prepared. Run 'loki update apply', then retry 'loki connection setup openai'")
	}
	return errors.New(message +
		"Update the appliance first:\n" +
		"  loki update prepare\n" +
		"  loki update apply\n" +
		"Then retry 'loki connection setup openai'")
}

func connectionSetupCompatibilityFromStatus(frontendRelease string, result windowshost.OperatorResult) error {
	if result.Probe.ExitCode != 0 {
		detail := progress.NonProgressText(result.Probe.Stderr)
		if detail == "" {
			detail = strings.TrimSpace(result.Probe.Stdout)
		}
		if detail == "" {
			detail = fmt.Sprintf("status command exited with code %d", result.Probe.ExitCode)
		}
		return fmt.Errorf("cannot verify the Loki appliance before OpenAI setup: %s\nRun 'loki update status' to inspect the appliance, then retry", detail)
	}
	status, err := windowshost.ParseOperatorStatus([]byte(result.Probe.Stdout))
	if err != nil {
		return fmt.Errorf("cannot verify the Loki appliance before OpenAI setup: %w\nRun 'loki update status' to inspect the appliance, then retry", err)
	}
	// DistributionVersion identifies the immutable initial WSL image, not
	// the installed lifecycle generation. Only live status reports updates.
	return connectionSetupCompatibilityError(frontendRelease, status.Release, status.UpdatePrepared)
}

func normalizedReleaseTag(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return "v" + strings.TrimPrefix(value, "v")
}
