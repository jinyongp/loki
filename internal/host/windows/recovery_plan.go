package windows

type RecoveryStep string

const (
	RecoveryRemoveStartupTask  RecoveryStep = "remove-startup-task"
	RecoveryTerminateDistro    RecoveryStep = "terminate-distribution"
	RecoveryUnregisterDistro   RecoveryStep = "unregister-distribution"
	RecoveryVerifyDistroGone   RecoveryStep = "verify-distribution-absent"
	RecoveryRemoveWindowsState RecoveryStep = "remove-windows-state"
	RecoveryVerifyFresh        RecoveryStep = "verify-fresh"
)

type RecoveryPlan struct {
	Action ExistingAction
	Steps  []RecoveryStep
}

func BuildRecoveryPlan(snapshot ExistingSnapshot, staleApproved bool) RecoveryPlan {
	assessment := AssessExistingInstallation(snapshot)
	plan := RecoveryPlan{Action: assessment.Action}
	switch assessment.Action {
	case ExistingRemoveOrphan:
		if snapshot.StartupTask.Present {
			plan.Steps = append(plan.Steps, RecoveryRemoveStartupTask)
		}
		if snapshot.Windows.Present {
			plan.Steps = append(plan.Steps, RecoveryRemoveWindowsState)
		}
		if len(plan.Steps) > 0 {
			plan.Steps = append(plan.Steps, RecoveryVerifyFresh)
		}
	case ExistingStaleNeedsApproval:
		if !staleApproved {
			return plan
		}
		if snapshot.StartupTask.Present {
			plan.Steps = append(plan.Steps, RecoveryRemoveStartupTask)
		}
		plan.Steps = append(plan.Steps,
			RecoveryTerminateDistro,
			RecoveryUnregisterDistro,
			RecoveryVerifyDistroGone,
		)
		if snapshot.Windows.Present {
			plan.Steps = append(plan.Steps, RecoveryRemoveWindowsState)
		}
		plan.Steps = append(plan.Steps, RecoveryVerifyFresh)
	}
	return plan
}

type StartupTaskProbe struct {
	Present     bool
	Running     bool
	Description string
	Actions     []StartupTaskAction
}

type StartupTaskAction struct {
	Executable string
	Arguments  string
}

func ClassifyStartupTask(probe StartupTaskProbe, expected ExpectedInstallation) StartupTaskState {
	if !probe.Present {
		return StartupTaskState{}
	}
	owned := len(probe.Actions) == 1 &&
		matchesStartupAction(probe.Actions[0].Executable, probe.Actions[0].Arguments, expected) &&
		probe.Description == "Keep the Loki WSL2 appliance running."
	return StartupTaskState{Present: true, Owned: owned, Running: probe.Running}
}
