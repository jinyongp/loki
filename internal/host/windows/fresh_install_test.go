package windows

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"loki/internal/progress"
)

type fakeFreshPlatform struct {
	failStage    string
	partialStage string
	calls        []string
	rollback     []InstallTransaction
}

func (platform *fakeFreshPlatform) fail(stage string) error {
	platform.calls = append(platform.calls, stage)
	if platform.failStage == stage {
		return errors.New("synthetic " + stage + " failure")
	}
	return nil
}

func (platform *fakeFreshPlatform) PrepareAppliance(context.Context, InstallOptions) (PreparedAppliance, func(), error) {
	if err := platform.fail("prepare"); err != nil {
		return PreparedAppliance{}, nil, err
	}
	return PreparedAppliance{Path: "fixture.wsl"}, func() { platform.calls = append(platform.calls, "cleanup") }, nil
}
func (platform *fakeFreshPlatform) RegisterDistribution(context.Context, ExpectedInstallation, InstallOptions, PreparedAppliance) (bool, error) {
	err := platform.fail("register")
	if err != nil {
		return platform.partialStage == "register", err
	}
	return true, nil
}
func (platform *fakeFreshPlatform) Provision(context.Context, ExpectedInstallation, InstallOptions) (ConnectionMaterial, error) {
	if err := platform.fail("provision"); err != nil {
		return ConnectionMaterial{}, err
	}
	return ConnectionMaterial{LocalOrigin: "http://127.0.0.1:18765/mcp", Token: "secret"}, nil
}
func (platform *fakeFreshPlatform) PublishWindowsState(context.Context, ExpectedInstallation, InstallOptions, ConnectionMaterial) (bool, error) {
	err := platform.fail("state")
	if err != nil {
		return true, err
	}
	return true, nil
}
func (platform *fakeFreshPlatform) CreateStartupTask(context.Context, ExpectedInstallation) (bool, error) {
	err := platform.fail("task")
	if err != nil {
		return true, err
	}
	return true, nil
}
func (platform *fakeFreshPlatform) PublishOwnership(context.Context, ExpectedInstallation, InstallOptions) error {
	return platform.fail("ownership")
}
func (platform *fakeFreshPlatform) RollbackFresh(_ context.Context, _ ExpectedInstallation, _ InstallOptions, transaction InstallTransaction) error {
	platform.calls = append(platform.calls, "rollback")
	platform.rollback = append(platform.rollback, transaction)
	return nil
}

func TestTransactionalFreshInstallerRollsBackOnlyAfterRegistration(t *testing.T) {
	expected := fixtureExpected()
	for _, stage := range []string{"prepare", "register", "provision", "state", "task", "ownership"} {
		t.Run(stage, func(t *testing.T) {
			platform := &fakeFreshPlatform{failStage: stage}
			installer := TransactionalFreshInstaller{Platform: platform}
			err := installer.Install(context.Background(), expected, InstallOptions{
				Distribution: expected.Distribution, MCPPort: 18765, AutoStart: true,
				InstallLocation: `D:\Loki\loki-mcp`,
			})
			if err == nil {
				t.Fatal("synthetic failure was ignored")
			}
			wantRollback := stage != "prepare" && stage != "register"
			if (len(platform.rollback) != 0) != wantRollback {
				t.Fatalf("stage=%s rollback=%#v calls=%#v", stage, platform.rollback, platform.calls)
			}
			if wantRollback {
				transaction := platform.rollback[0]
				if !transaction.CreatedDistribution {
					t.Fatalf("rollback lost distribution ownership %#v", transaction)
				}
				if stage == "provision" && (transaction.CreatedStateDir || transaction.CreatedStartupTask) {
					t.Fatalf("provision failure claimed later resources %#v", transaction)
				}
			}
		})
	}
}

func TestTransactionalFreshInstallerRollsBackPartialRegistration(t *testing.T) {
	expected := fixtureExpected()
	platform := &fakeFreshPlatform{failStage: "register", partialStage: "register"}
	installer := TransactionalFreshInstaller{Platform: platform}
	err := installer.Install(context.Background(), expected, InstallOptions{Distribution: expected.Distribution, MCPPort: 18765})
	if err == nil {
		t.Fatal("partial registration failure was ignored")
	}
	if len(platform.rollback) != 1 || !platform.rollback[0].CreatedDistribution {
		t.Fatalf("partial registration was not rolled back: %#v", platform.rollback)
	}
}

func TestTransactionalFreshInstallerSuccessAndAutoStartDisabled(t *testing.T) {
	expected := fixtureExpected()
	platform := &fakeFreshPlatform{}
	installer := TransactionalFreshInstaller{Platform: platform}
	if err := installer.Install(context.Background(), expected, InstallOptions{
		Distribution: expected.Distribution, MCPPort: 18765, AutoStart: false,
	}); err != nil {
		t.Fatal(err)
	}
	if len(platform.rollback) != 0 {
		t.Fatalf("successful install rolled back %#v", platform.rollback)
	}
	want := []string{"prepare", "register", "provision", "state", "ownership", "cleanup"}
	if !reflect.DeepEqual(platform.calls, want) {
		t.Fatalf("calls=%#v want=%#v", platform.calls, want)
	}
}

func TestTransactionalFreshInstallerReportsPhases(t *testing.T) {
	expected := fixtureExpected()
	platform := &fakeFreshPlatform{}
	var phases []string
	reporter := progress.ReporterFunc(func(event progress.Event) {
		phases = append(phases, event.Phase)
	})
	installer := TransactionalFreshInstaller{Platform: platform, Progress: reporter}
	if err := installer.Install(t.Context(), expected, InstallOptions{
		Distribution: expected.Distribution, MCPPort: 18765, AutoStart: true,
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"prepare-appliance", "register", "provision", "publish-state", "startup", "ownership"}
	if !reflect.DeepEqual(phases, want) {
		t.Fatalf("phases=%#v want=%#v", phases, want)
	}
}
