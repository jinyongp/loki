package management

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"loki/internal/config"
	"loki/internal/execution"
	hostpolicy "loki/internal/host/policy"
	"loki/internal/tools"
)

func fullRuntimeConfiguration() []byte {
	return []byte("root = \"/workspace\"\nhost = \"127.0.0.1\"\nport = 18765\naudit_log = \"/var/lib/loki/mcp/audit.jsonl\"\n")
}

func (s Store) fullApplicationInputs() (config.Config, execution.Contract, error) {
	resources, err := s.FullResources()
	if err != nil {
		return config.Config{}, execution.Contract{}, err
	}
	input := fullRuntimeConfiguration()
	for _, choice := range resources.Plan.Enabled {
		if choice.ID == "github" {
			fragment, err := s.ProviderConfiguration("github")
			if err != nil {
				return config.Config{}, execution.Contract{}, err
			}
			input = append(input, fragment...)
		}
	}
	c, err := config.Parse(input)
	if err != nil {
		return c, execution.Contract{}, err
	}
	relative, exists := resources.Payloads["runtime-core"].Assets["execution-contract"]
	if !exists {
		return c, execution.Contract{}, fmt.Errorf("runtime execution contract is unavailable")
	}
	path, err := resources.Plan.Program("runtime-core", filepath.FromSlash(relative))
	if err != nil {
		return c, execution.Contract{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return c, execution.Contract{}, err
	}
	contract, err := execution.Load(raw)
	return c, contract, err
}

func (s Store) PrepareApplicationResources(change hostpolicy.ApplicationChange) (hostpolicy.ApplicationPlan, error) {
	state, err := s.Load()
	if err != nil {
		return hostpolicy.ApplicationPlan{}, err
	}
	c, contract, err := s.fullApplicationInputs()
	if err != nil {
		return hostpolicy.ApplicationPlan{}, err
	}
	return hostpolicy.PrepareApplicationChange(c, contract, state.ApplicationResources, change)
}

// ApplyApplicationResources publishes grants in the same atomic activation
// snapshot as tools. The existing transaction journal and deployment stop
// intent make crash recovery retain the old reservation until exact cleanup.
func (s Store) ApplyApplicationResources(ctx context.Context, plan hostpolicy.ApplicationPlan) error {
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	previousTransaction, err := s.readTransaction()
	if err != nil {
		return err
	}
	if previousTransaction != nil && previousTransaction.Action == "resource_policy" && previousTransaction.Phase != tools.Committed && previousTransaction.Phase != tools.Aborted {
		if err := s.recoverTransaction(); err != nil {
			return err
		}
	}
	if err := s.mutable(); err != nil {
		return err
	}
	state, err := s.Load()
	if err != nil {
		return err
	}
	c, contract, err := s.fullApplicationInputs()
	if err != nil {
		return err
	}
	current := state.ApplicationResources
	// If publication succeeded but its response/start was lost, validate the
	// exact original before/after pair from the existing transaction journal.
	if previousTransaction != nil && previousTransaction.Action == "resource_policy" && snapshotDigest(state) == snapshotDigest(previousTransaction.Candidate) {
		if _, err := hostpolicy.ValidateApplicationPlan(c, contract, previousTransaction.Previous.ApplicationResources, plan); err == nil {
			return nil
		}
	}
	next, err := hostpolicy.ValidateApplicationPlan(c, contract, current, plan)
	if err != nil {
		return err
	}
	candidate := state
	candidate.ApplicationResources = next
	if snapshotDigest(candidate) == snapshotDigest(state) {
		return nil
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	// Stop happens before publication. Its persistent original stop intent is
	// also the recovery basis when a later service start is interrupted.
	if err := s.pauseFullMutationLocked(ctx, false); err != nil {
		return err
	}
	t := transaction{Schema: 1, ID: fmt.Sprintf("op-%d", time.Now().UnixNano()), Action: "resource_policy", Phase: tools.Staged, Previous: state, Candidate: candidate}
	if err := t.validate(); err != nil {
		return err
	}
	journal := filepath.Join(s.Root, "transaction.json")
	if err := atomicJSON(journal, t); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.Save(candidate); err != nil {
		return err
	}
	t.Phase = tools.Committed
	return atomicJSON(journal, t)
}
