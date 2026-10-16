package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"loki/internal/fault"
	"loki/internal/tools"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ownedFixture(t *testing.T, s Store, id tools.ID, requires ...tools.ID) Installation {
	return ownedFixtureTarget(t, s, LocalTarget(tools.ProjectHost), id, requires...)
}

func TestStatusReportsSelectedFeaturesWithoutRuntimeProbe(t *testing.T) {
	s := Store{Root: t.TempDir()}
	state, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	installation := ownedFixture(t, s, "browser")
	installation.Manifest.Capabilities = []string{"devtools", "pdf"}
	state.Installed["browser"] = installation
	state.Config.Tools = []tools.Selection{{ID: "browser", Enabled: false, Capabilities: []string{"devtools", "pdf"}}}
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	report, err := s.Status()
	if err != nil {
		t.Fatal(err)
	}
	observed := report.Tools["browser"]
	if observed.Enabled || observed.Readiness != tools.Unknown || strings.Join(observed.Capabilities, ",") != "devtools,pdf" {
		t.Fatal("status lost feature selection or claimed readiness", observed)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Report
	if err := json.Unmarshal(raw, &decoded); err != nil || strings.Join(decoded.Tools["browser"].Capabilities, ",") != "devtools,pdf" {
		t.Fatal("JSON report lost feature selection", err)
	}
}

func ownedFixtureTarget(t *testing.T, s Store, target tools.Target, id tools.ID, requires ...tools.ID) Installation {
	t.Helper()
	a := tools.Artifact{Module: id, Release: Release, Target: target, URL: "https://example.com/tool.zip", SHA256: strings.Repeat("a", 64), Bytes: 1, Format: "zip"}
	m := tools.Manifest{Schema: 1, ID: id, Release: Release, Targets: []tools.Target{a.Target}, Requires: requires, Tools: []string{}}
	dir, err := s.Generation(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(dir, ".loki-owner.json"), Owner{Schema: 1, Identity: a.Identity()}); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(dir, "module.json"), m); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(dir, ".loki-generation.json"), generationRecord{Schema: 1, PreparedAt: time.Now().UTC(), Installation: Installation{Artifact: a, Manifest: m}}); err != nil {
		t.Fatal(err)
	}
	return Installation{Artifact: a, Manifest: m}
}

func TestDoctorDistinguishesUnprobedPrivateDependencies(t *testing.T) {
	s := Store{Root: t.TempDir()}
	state, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.Installed["worker"] = ownedFixture(t, s, "worker")
	state.Installed["browser"] = ownedFixture(t, s, "browser", "worker")
	state.Config.Tools = []tools.Selection{{ID: "browser", Enabled: true}}
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	status, err := s.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Healthy != nil || status.Tools["browser"].Readiness != tools.Unknown {
		t.Fatal("status claimed runtime health")
	}
	report, err := s.Doctor(context.Background(), map[tools.ID]Probe{"browser": func(context.Context, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if report.Tools["browser"].Readiness != tools.Unknown || report.Tools["worker"].Enabled || report.Ready == nil || *report.Ready {
		t.Fatal("unprobed private dependency became publicly ready")
	}
	report, err = s.Doctor(context.Background(), map[tools.ID]Probe{
		"browser": func(context.Context, string) error { return nil },
		"worker":  func(context.Context, string) error { return errors.New("native library missing") },
	})
	if err != nil {
		t.Fatal(err)
	}
	if *report.Healthy || report.Tools["browser"].Readiness != tools.Degraded {
		t.Fatal("private dependency failure did not degrade browser")
	}
	if err := s.Remove("worker"); err == nil {
		t.Fatal("removed a live prerequisite")
	}
}

func TestDoctorPinsRunningProbeUntilItFinishes(t *testing.T) {
	s := Store{Root: t.TempDir()}
	state, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.Installed["browser"] = ownedFixture(t, s, "browser")
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	called := false
	report, err := s.Doctor(context.Background(), map[tools.ID]Probe{
		"browser": func(context.Context, string) error {
			called = true
			if err := s.Remove("browser"); !errors.Is(err, ErrGenerationInUse) {
				t.Fatalf("probe resources could be removed: %v", err)
			}
			return nil
		},
	})
	if err != nil || !called || report.Healthy == nil || !*report.Healthy {
		t.Fatalf("probe did not complete: %+v %v", report, err)
	}
	if err := s.Remove("browser"); err != nil {
		t.Fatalf("completed probe retained its lease: %v", err)
	}
}

func TestDoctorReportsInterruptedJournalAndOwnership(t *testing.T) {
	s := Store{Root: t.TempDir()}
	state, _ := s.Load()
	state.Installed["browser"] = ownedFixture(t, s, "browser")
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	op := tools.Operation{Schema: 1, ID: "op-fixture", Module: "browser", Action: "install", Phase: tools.Prepared, Candidate: strings.Repeat("a", 64)}
	if err := atomicJSON(filepath.Join(s.Root, "operation.json"), op); err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Generation(state.Installed["browser"].Artifact)
	if err := atomicJSON(filepath.Join(dir, ".loki-owner.json"), Owner{Schema: 1, Identity: "other"}); err != nil {
		t.Fatal(err)
	}
	report, err := s.Doctor(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if *report.Healthy || len(report.Issues) != 2 || report.Tools["browser"].Readiness != tools.Degraded {
		t.Fatalf("missing diagnosis: %+v", report)
	}
}

func TestStateRejectsUnknownFieldsAndTrailingDocuments(t *testing.T) {
	s := Store{Root: t.TempDir()}
	state, _ := s.Load()
	data, _ := json.Marshal(state)
	for _, invalid := range [][]byte{append(data, []byte("{}")...), append([]byte(`{"unexpected":true,`), data[1:]...)} {
		if err := os.MkdirAll(s.ControlDirectory(), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.ActivationPath(), invalid, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Load(); err == nil {
			t.Fatal("accepted ambiguous state")
		}
	}
}

func TestDoctorPreservesSafeStoreRecovery(t *testing.T) {
	s := Store{Root: t.TempDir()}
	state, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.Installed["browser"] = ownedFixture(t, s, "browser")
	state.Config.Tools = []tools.Selection{{ID: "browser", Enabled: true}}
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	want := fault.StoreRecovery{Schema: 1, Owner: "github", Reason: "missing_key"}
	r, err := s.Doctor(t.Context(), map[tools.ID]Probe{"browser": func(context.Context, string) error { return fmt.Errorf("owned runtime: %w", &want) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.StoreRecovery) != 1 || r.StoreRecovery[0] != want || r.Healthy == nil || *r.Healthy {
		t.Fatalf("doctor lost recovery details: %+v", r)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Report
	if err := json.Unmarshal(raw, &decoded); err != nil || len(decoded.StoreRecovery) != 1 {
		t.Fatal("recovery lost in JSON", err)
	}
}
