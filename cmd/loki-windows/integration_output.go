package main

import (
	"encoding/json"
	"fmt"
	"io"

	"loki/internal/host/githubsetup"
	windowshost "loki/internal/host/windows"
)

type windowsIntegrationReport struct {
	SchemaVersion    int                     `json:"schema_version"`
	Name             string                  `json:"name"`
	Configured       bool                    `json:"configured"`
	Enabled          bool                    `json:"enabled"`
	Ready            bool                    `json:"ready"`
	State            string                  `json:"state"`
	PublicKey        string                  `json:"public_key,omitempty"`
	Fingerprint      string                  `json:"fingerprint,omitempty"`
	IdentityName     string                  `json:"identity_name,omitempty"`
	IdentityEmail    string                  `json:"identity_email,omitempty"`
	GitHubAppID      int64                   `json:"github_app_id,omitempty"`
	TargetCount      int                     `json:"target_count,omitempty"`
	Authentication   string                  `json:"authentication,omitempty"`
	Detail           string                  `json:"detail,omitempty"`
	AppReady         bool                    `json:"app_ready,omitempty"`
	PersonalProjects *githubsetup.UserStatus `json:"personal_projects,omitempty"`
}

// Doctor reports are useful precisely when exit status is nonzero. Decode them
// before handling generic process errors and preserve the diagnostic exit code.
func writeWindowsIntegrationInspection(action string, probe windowshost.NativeProbe, machine bool, stdout, stderr io.Writer) int {
	if machine {
		return writeNativeProbe(probe, stdout, stderr)
	}
	if err := windowshost.ValidateOperatorInfoSchema([]byte(probe.Stdout)); err != nil {
		if probe.ExitCode != 0 {
			return writeNativeProbe(probe, stdout, stderr)
		}
		fmt.Fprintln(stderr, "Cannot decode Loki integration report:", err)
		return 1
	}
	if action == "list" {
		var envelope struct {
			Integrations []windowsIntegrationReport `json:"integrations"`
		}
		if err := json.Unmarshal([]byte(probe.Stdout), &envelope); err != nil {
			fmt.Fprintln(stderr, "Cannot decode Loki integration list.")
			return 1
		}
		fmt.Fprintln(stdout, "Loki integrations")
		for _, report := range envelope.Integrations {
			fmt.Fprintf(stdout, "  %s: %s\n", report.Name, report.State)
		}
	} else {
		var report windowsIntegrationReport
		if err := json.Unmarshal([]byte(probe.Stdout), &report); err != nil || report.Name == "" || report.State == "" {
			fmt.Fprintln(stderr, "Cannot decode Loki integration status.")
			return 1
		}
		renderWindowsIntegration(report, stdout)
	}
	if probe.Stderr != "" {
		fmt.Fprintln(stderr, probe.Stderr)
	}
	if probe.ExitCode < 0 {
		return 1
	}
	return probe.ExitCode
}

func renderWindowsIntegration(report windowsIntegrationReport, stdout io.Writer) {
	fmt.Fprintf(stdout, "Loki integration: %s\n", report.Name)
	fmt.Fprintf(stdout, "  State: %s\n", report.State)
	fmt.Fprintf(stdout, "  Configured: %t\n", report.Configured)
	fmt.Fprintf(stdout, "  Enabled: %t\n", report.Enabled)
	fmt.Fprintf(stdout, "  Ready: %t\n", report.Ready)
	if report.PublicKey != "" {
		fmt.Fprintf(stdout, "  Public key: %s\n", report.PublicKey)
		fmt.Fprintf(stdout, "  Fingerprint: %s\n", report.Fingerprint)
		fmt.Fprintf(stdout, "  Identity: %s <%s>\n", report.IdentityName, report.IdentityEmail)
	}
	if report.GitHubAppID != 0 {
		fmt.Fprintf(stdout, "  App ID: %d\n", report.GitHubAppID)
		fmt.Fprintf(stdout, "  Repositories: %d\n", report.TargetCount)
	}
	if report.Authentication != "" {
		fmt.Fprintf(stdout, "  Authentication: %s\n", report.Authentication)
	}
	if report.PersonalProjects != nil {
		fmt.Fprintf(stdout, "  App ready: %t\n", report.AppReady)
		githubsetup.RenderUserStatus(stdout, *report.PersonalProjects)
	}
	if report.Detail != "" {
		fmt.Fprintf(stdout, "  Detail: %s\n", report.Detail)
	}
}
