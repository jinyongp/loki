package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"loki/internal/tools"
	"os"
	"path/filepath"
	"slices"
)

// Status never runs a tool or claims observed readiness. Doctor performs owned
// resource checks and invokes only explicitly supplied module probes.
type Report struct {
	Installed   bool                     `json:"installed"`
	Release     string                   `json:"release"`
	Target      tools.Target             `json:"target"`
	Tools       map[tools.ID]tools.State `json:"tools"`
	Issues      []string                 `json:"issues"`
	Healthy     *bool                    `json:"healthy,omitempty"`
	Ready       *bool                    `json:"ready,omitempty"`
	Deployments []DeploymentReservation  `json:"deployments"`
}

type Probe func(context.Context, string) error

func (s Store) Status() (Report, error) {
	state, err := s.Load()
	if err != nil {
		return Report{}, err
	}
	return s.status(state)
}

func (s Store) status(state Snapshot) (Report, error) {
	_, err := os.Stat(s.ActivationPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Report{}, err
	}
	r := Report{Installed: err == nil, Release: state.Config.Release, Target: LocalTarget(state.Config.Mode), Tools: map[tools.ID]tools.State{}, Issues: []string{}}
	r.Deployments, err = s.Deployments()
	if err != nil {
		return r, fmt.Errorf("deployment reservations cannot be inspected: %w", err)
	}
	for id, installation := range state.Installed {
		r.Tools[id] = tools.State{Installed: true, Release: installation.Artifact.Release, Readiness: tools.Unknown}
	}
	for _, selection := range state.Config.Tools {
		observed := r.Tools[selection.ID]
		observed.Enabled = selection.Enabled
		r.Tools[selection.ID] = observed
	}
	return r, nil
}

func (s Store) Doctor(ctx context.Context, probes map[tools.ID]Probe) (Report, error) {
	state, err := s.Load()
	if err != nil {
		return Report{}, err
	}
	r, err := s.status(state)
	if err != nil {
		return r, err
	}
	healthy := true
	r.Healthy = &healthy
	issue := func(message string) { healthy = false; r.Issues = append(r.Issues, message) }
	if !r.Installed {
		issue("management is not installed; run loki install")
	}
	if journal, err := s.readRestoreJournal(); err != nil {
		issue("backup restore journal is invalid: " + err.Error())
	} else if journal != nil && journal.Phase != "committed" {
		issue(fmt.Sprintf("backup restore %s is interrupted; run loki restore %s", journal.ID, journal.ID))
	}
	if publishing, err := s.readManagerPublication(); err != nil {
		issue("manager publication is invalid: " + err.Error())
	} else if publishing != nil && publishing.Phase != tools.Committed && publishing.Phase != tools.Aborted {
		issue(fmt.Sprintf("manager installation %s was interrupted; run bundled loki tools recover", publishing.ID))
	} else if publishing != nil && publishing.Phase == tools.Committed {
		digest, size, err := managerDigest(publishing.Candidate.Executable)
		if err != nil || digest != publishing.Candidate.SHA256 || size != publishing.Candidate.Bytes {
			issue("installed manager command is missing or differs from its ownership record")
		}
		var record ManagerRecord
		if err := readOwnedJSON(publishing.Candidate.Executable+".loki-owner.json", tools.MaxManifestBytes, &record); err != nil || record != publishing.Candidate {
			issue("installed manager ownership marker differs from its completed publication")
		}
	}
	if retiring, err := s.readRetirement(); err != nil {
		issue("retirement journal is invalid: " + err.Error())
	} else if retiring != nil && retiring.Phase != tools.Committed && retiring.Phase != tools.Aborted {
		issue(fmt.Sprintf("cleanup %s was interrupted; run loki tools recover", retiring.ID))
	}
	if t, err := s.readTransaction(); err != nil {
		issue("tool transaction is invalid: " + err.Error())
	} else if t != nil && t.Phase != tools.Committed && t.Phase != tools.Aborted {
		issue(fmt.Sprintf("transaction %s was interrupted; run loki tools recover", t.ID))
	}
	journal, err := os.ReadFile(filepath.Join(s.Root, "operation.json"))
	if err == nil {
		var op tools.Operation
		if json.Unmarshal(journal, &op) != nil || op.Validate() != nil {
			issue("operation journal is invalid; repair requires inspection")
		} else if op.Phase != tools.Committed && op.Phase != tools.Aborted {
			issue(fmt.Sprintf("operation %s was interrupted; run loki tools recover", op.ID))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		issue("operation journal cannot be read: " + err.Error())
	}
	ids := make([]tools.ID, 0, len(r.Tools))
	for id := range r.Tools {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		observed := r.Tools[id]
		installation, exists := state.Installed[id]
		var checkErr error
		if !exists {
			checkErr = fmt.Errorf("selected tool is not installed")
		} else {
			generation, err := s.Generation(installation.Artifact)
			checkErr = err
			if checkErr == nil {
				checkErr = verifyOwner(generation, installation.Artifact)
			}
			if checkErr == nil {
				var data []byte
				data, checkErr = os.ReadFile(filepath.Join(generation, "module.json"))
				if checkErr == nil {
					var manifest tools.Manifest
					manifest, checkErr = tools.ParseManifest(data)
					if checkErr == nil && !sameManifest(manifest, installation.Manifest) {
						checkErr = fmt.Errorf("installed module manifest differs from state")
					}
				}
			}
			if checkErr == nil && installation.Artifact.Target != r.Target {
				checkErr = fmt.Errorf("artifact does not match this execution host")
			}
			if checkErr == nil && probes[id] != nil {
				checkErr = s.probeGeneration(ctx, installation.Artifact, generation, probes[id])
			}
		}
		if checkErr != nil {
			observed.Readiness, observed.Reason = tools.Degraded, checkErr.Error()
			issue(fmt.Sprintf("%s: %s", id, observed.Reason))
		} else if probes[id] != nil {
			observed.Readiness = tools.Ready
		} else {
			observed.Reason = "owned resources checked; runtime probe is not available"
		}
		r.Tools[id] = observed
	}
	// Propagate resource failures through private prerequisites, including
	// prerequisites whose public exposure is intentionally disabled.
	for pass := 0; pass < len(ids); pass++ {
		changed := false
		for _, id := range ids {
			observed := r.Tools[id]
			if observed.Readiness == tools.Degraded {
				continue
			}
			for _, dependency := range state.Installed[id].Manifest.Requires {
				dep, exists := r.Tools[dependency]
				if !exists || dep.Readiness == tools.Degraded {
					observed.Readiness = tools.Degraded
					observed.Reason = fmt.Sprintf("prerequisite %s is missing or degraded", dependency)
					issue(fmt.Sprintf("%s: %s", id, observed.Reason))
					r.Tools[id] = observed
					changed = true
					break
				}
				if dep.Readiness != tools.Ready && observed.Readiness == tools.Ready {
					observed.Readiness = tools.Unknown
					observed.Reason = fmt.Sprintf("prerequisite %s runtime has not been probed", dependency)
					r.Tools[id] = observed
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}
	ready := healthy
	for _, observed := range r.Tools {
		if observed.Enabled && observed.Readiness != tools.Ready {
			ready = false
		}
	}
	r.Ready = &ready
	return r, nil
}

func (s Store) probeGeneration(ctx context.Context, artifact tools.Artifact, generation string, probe Probe) error {
	release, err := s.Lease(artifact)
	if err != nil {
		return err
	}
	defer release()
	return probe(ctx, generation)
}

func sameManifest(a, b tools.Manifest) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}
