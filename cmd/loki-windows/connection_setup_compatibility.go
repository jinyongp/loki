package main

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/mod/semver"
)

func connectionSetupCompatibilityError(frontendRelease, applianceVersion string, updatePrepared bool) error {
	frontend := normalizedReleaseTag(frontendRelease)
	appliance := normalizedReleaseTag(applianceVersion)
	if !semver.IsValid(frontend) || !semver.IsValid(appliance) {
		return errors.New("cannot verify Loki frontend/appliance release compatibility; run 'loki update status' and retry")
	}
	switch semver.Compare(appliance, frontend) {
	case 0:
		return nil
	case -1:
		if updatePrepared {
			return fmt.Errorf(
				"Loki appliance %s is older than Windows frontend %s.\n"+
					"An appliance update is already prepared. Run 'loki update apply', then retry 'loki connection setup openai'",
				appliance, frontend,
			)
		}
		return fmt.Errorf(
			"Loki appliance %s is older than Windows frontend %s.\n"+
				"Update the appliance first:\n"+
				"  loki update prepare\n"+
				"  loki update apply\n"+
				"Then retry 'loki connection setup openai'",
			appliance, frontend,
		)
	default:
		return fmt.Errorf(
			"Windows frontend %s is older than Loki appliance %s.\n"+
				"Run 'loki update', then retry 'loki connection setup openai'",
			frontend, appliance,
		)
	}
}

func normalizedReleaseTag(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return "v" + strings.TrimPrefix(value, "v")
}
