package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	windowshost "loki/internal/host/windows"
)

type applianceAdvisoryState string

const (
	applianceAdvisoryCurrent  applianceAdvisoryState = "current"
	applianceAdvisoryPrepare  applianceAdvisoryState = "prepare"
	applianceAdvisoryPrepared applianceAdvisoryState = "prepared"
	applianceAdvisoryNewer    applianceAdvisoryState = "newer"
)

type applianceUpdateAdvisory struct {
	State     applianceAdvisoryState
	Installed string
	Published string
}

type applianceAdvisoryDependencies struct {
	Status  func(context.Context) (windowshost.OperatorStatus, error)
	Resolve func(context.Context) (windowshost.ApplianceReleasePointer, error)
	Compare func(string, windowshost.ApplianceReleasePointer) (int, error)
}

func resolveApplianceUpdateAdvisory(ctx context.Context, deps applianceAdvisoryDependencies) (applianceUpdateAdvisory, error) {
	if deps.Status == nil || deps.Resolve == nil || deps.Compare == nil {
		return applianceUpdateAdvisory{}, errors.New("appliance update advisory is not configured")
	}
	status, err := deps.Status(ctx)
	if err != nil {
		return applianceUpdateAdvisory{}, err
	}
	installed := strings.TrimSpace(status.Release)
	if installed == "" {
		return applianceUpdateAdvisory{}, errors.New("Loki appliance release is missing from status")
	}
	if status.UpdatePrepared {
		return applianceUpdateAdvisory{State: applianceAdvisoryPrepared, Installed: installed}, nil
	}
	if status.UpdateAvailable {
		return applianceUpdateAdvisory{State: applianceAdvisoryPrepare, Installed: installed}, nil
	}
	pointer, err := deps.Resolve(ctx)
	if err != nil {
		return applianceUpdateAdvisory{}, err
	}
	comparison, err := deps.Compare(installed, pointer)
	if err != nil {
		return applianceUpdateAdvisory{}, err
	}
	advisory := applianceUpdateAdvisory{
		Installed: installed,
		Published: pointer.ReleaseTag,
	}
	switch {
	case comparison < 0:
		advisory.State = applianceAdvisoryPrepare
	case comparison == 0:
		advisory.State = applianceAdvisoryCurrent
	default:
		advisory.State = applianceAdvisoryNewer
	}
	return advisory, nil
}

func renderApplianceUpdateAdvisory(advisory applianceUpdateAdvisory, output io.Writer) error {
	switch advisory.State {
	case applianceAdvisoryCurrent:
		_, err := fmt.Fprintf(output, "Loki appliance %s is current.\n", advisory.Installed)
		return err
	case applianceAdvisoryPrepare:
		if advisory.Published != "" {
			if _, err := fmt.Fprintf(output, "Loki appliance %s has an update available at %s.\n", advisory.Installed, advisory.Published); err != nil {
				return err
			}
		} else if _, err := fmt.Fprintf(output, "Loki appliance %s has an update available.\n", advisory.Installed); err != nil {
			return err
		}
		_, err := fmt.Fprintln(output, "Run 'loki update prepare' to prepare the appliance update.")
		return err
	case applianceAdvisoryPrepared:
		if _, err := fmt.Fprintln(output, "A Loki appliance update is already prepared."); err != nil {
			return err
		}
		_, err := fmt.Fprintln(output, "Run 'loki update apply' to review and apply it.")
		return err
	case applianceAdvisoryNewer:
		_, err := fmt.Fprintf(output, "Loki appliance %s is newer than the published appliance release %s; no prepare is required.\n", advisory.Installed, advisory.Published)
		return err
	default:
		return errors.New("Loki appliance update advisory state is invalid")
	}
}
