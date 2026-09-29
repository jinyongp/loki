package main

import (
	"errors"
	"fmt"
)

func updateApplyReadinessError(status machineUpdateStatus) error {
	if status.Prepared != nil {
		if status.Available == nil {
			return errors.New("prepared Loki appliance update is missing its candidate release")
		}
		if !status.UpdateAvailable {
			return errors.New("prepared Loki appliance update does not advance the installed release")
		}
		return nil
	}
	if !status.UpdateAvailable {
		if status.Installed != nil {
			return fmt.Errorf(
				"no newer Loki appliance release is available to apply; installed release is %s",
				generationVersion(status.Installed),
			)
		}
		return errors.New("no Loki appliance update is available to apply")
	}
	return errors.New("Loki appliance update is available but not prepared; run 'loki update prepare' first")
}
