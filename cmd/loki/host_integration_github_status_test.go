package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"loki/internal/host/githubsetup"
	"loki/internal/host/lifecycle"
	lifecyclecompose "loki/internal/host/lifecycle/compose"
)

type statusIntegrationRuntime struct {
	staticIntegrationRuntime
	view  githubsetup.UserView
	err   error
	calls int
}

func (r *statusIntegrationRuntime) GitHubUserAuthorization(_ context.Context, request githubsetup.UserRequest) (githubsetup.UserView, error) {
	if request.Action != "status" || request.Account != "" || request.SessionID != "" {
		panic("status forwarded a mutation or account selector")
	}
	r.calls++
	return r.view, r.err
}

func TestUnifiedGitHubStatusIncludesAuthorizationAndPreservesAppReadiness(t *testing.T) {
	for _, scenario := range []string{"ready", "refresh_required", "unconfigured", "expired", "unavailable", "wrong-account", "incomplete", "disabled", "runtime-down", "organization-only"} {
		t.Run(scenario, func(t *testing.T) {
			store, now := hostIntegrationStoreFixture(t)
			raw := githubConfigFixture()
			if scenario != "organization-only" {
				raw = []byte(strings.ReplaceAll(strings.ReplaceAll(string(raw), "example-org", "example-user"), `account_type = "organization"`, `account_type = "user"`))
			}
			configDigest, err := store.WriteManagedIntegrationFile(t.Context(), lifecycle.ManagedGitHubConfigFile, raw)
			if err != nil {
				t.Fatal(err)
			}
			credentialDigest, err := store.WriteManagedIntegrationFile(t.Context(), lifecycle.ManagedGitHubCredentialFile, []byte("synthetic-key"))
			if err != nil {
				t.Fatal(err)
			}
			managed := lifecycle.DefaultManagedIntegrationState()
			managed.GitHub = lifecycle.ManagedIntegrationToggle{Configured: true, Enabled: scenario != "disabled", ConfigSHA256: configDigest, CredentialSHA256: credentialDigest}
			if err := store.CommitManagedIntegrations(t.Context(), managed, "status-fixture", now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			status := scenario
			if scenario != "unconfigured" && scenario != "expired" && scenario != "refresh_required" {
				status = "ready"
			}
			aggregate := "ready"
			if status == "unconfigured" || status == "expired" {
				aggregate = "unconfigured"
			}
			runtime := &statusIntegrationRuntime{staticIntegrationRuntime: staticIntegrationRuntime{readiness: lifecyclecompose.RuntimeReadiness{Activated: true, GenerationID: "generation"}},
				view: githubsetup.UserView{Status: aggregate, Accounts: []githubsetup.UserAccountView{{Account: "example-user", Status: status, ExpiresAt: now.Add(time.Hour)}}}}
			if scenario == "unavailable" {
				runtime.err = errors.New("synthetic-private-token")
			}
			if scenario == "wrong-account" {
				runtime.view.Accounts[0].Account = "another-user"
			}
			if scenario == "incomplete" {
				runtime.view.Accounts = nil
			}
			if scenario == "runtime-down" {
				runtime.staticIntegrationRuntime.err = errors.New("down")
			}
			report, err := inspectHostIntegration(t.Context(), store, runtime, "github", true)
			if err != nil || report.PersonalProjects == nil {
				t.Fatal("unified status unavailable", report, err)
			}
			wantAppReady := scenario != "disabled" && scenario != "runtime-down"
			if report.Ready != wantAppReady || report.AppReady != wantAppReady {
				t.Fatal("App and personal readiness conflated", report)
			}
			if scenario == "organization-only" || scenario == "disabled" || scenario == "runtime-down" {
				if runtime.calls != 0 {
					t.Fatal("unnecessary user status query")
				}
			} else if runtime.calls != 1 {
				t.Fatal("status did not query personal authorization once", runtime.calls)
			}
			if scenario == "organization-only" && report.PersonalProjects.Status != "not_required" {
				t.Fatal("organization-only status needs personal authorization", report)
			}
			if scenario == "unconfigured" || scenario == "expired" {
				if report.State != "ready" || report.Detail != "" || len(report.PersonalProjects.Accounts) != 1 || report.PersonalProjects.Accounts[0].Status != scenario {
					t.Fatal("optional authorization blocked repository readiness or lost metadata", report)
				}
			}
			if scenario == "unavailable" || scenario == "wrong-account" || scenario == "incomplete" {
				if report.State != "ready" || report.PersonalProjects.Status != "unavailable" {
					t.Fatal("optional inspection failure affected repository readiness", report)
				}
			}
			encoded, _ := json.Marshal(report)
			if strings.Contains(string(encoded), "synthetic-private-token") {
				t.Fatal("status exposed private diagnostics")
			}
			runtime.calls = 0
			defaultReport, err := inspectHostIntegration(t.Context(), store, runtime, "github")
			if err != nil || runtime.calls != 0 || defaultReport.Ready != wantAppReady || defaultReport.PersonalProjects.Status != "not_requested" {
				t.Fatal("default status queried optional authorization or changed readiness", defaultReport, runtime.calls, err)
			}
		})
	}
}
