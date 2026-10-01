package lifecycle

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"loki/internal/progress"
)

func TestIntegrationProgressPrecedesMutationAndReportsRecovery(t *testing.T) {
	for _, kind := range []string{"browser", "signing", "github"} {
		for _, failHealth := range []bool{false, true} {
			t.Run(kind+"/"+map[bool]string{false: "success", true: "recovery"}[failHealth], func(t *testing.T) {
				store, backend, _, _, now, _ := transactionFixture(t)
				var phases []string
				reporter := progress.ReporterFunc(func(event progress.Event) {
					phases = append(phases, event.Phase)
					if event.Phase == "configure" && backend.nextSnapshot != 1 {
						t.Error("configuration progress was emitted before the recovery backup")
					}
					if strings.Contains(event.Message, "synthetic-private-key") {
						t.Error("private material appeared in progress")
					}
				})
				engine := &TransactionEngine{Store: store, Backend: backend, Progress: reporter, Now: func() time.Time { return now }}
				if failHealth {
					backend.healthErr = errors.New("synthetic health failure")
				}
				mutate := func(ctx context.Context, store *FileStore) error {
					if !slices.Contains(phases, "configure") {
						t.Error("configuration mutation began without progress")
					}
					_, err := store.WriteManagedIntegrationFile(ctx, ManagedGitHubCredentialFile, []byte("synthetic-private-key"))
					return err
				}
				var err error
				switch kind {
				case "browser":
					err = engine.SetComponent(t.Context(), "browser", false)
				case "signing":
					err = engine.UpdateManagedComponentIntegration(t.Context(), "signing", true, mutate)
				case "github":
					err = engine.UpdateManagedIntegration(t.Context(), "github", mutate)
				}
				if (err != nil) != failHealth || (failHealth && !strings.Contains(err.Error(), "synthetic health failure")) {
					t.Fatalf("err=%v failHealth=%t", err, failHealth)
				}
				if !slices.Contains(phases, "backup") || !slices.Contains(phases, "configure") || slices.Contains(phases, "recovery") != failHealth {
					t.Fatalf("missing progress stages: %v", phases)
				}
				if backend.nextSnapshot != 1 || backend.healthCalls == 0 || backend.restartCalls == 0 {
					t.Fatal("backup, restart, or health work was omitted")
				}
				if failHealth {
					if _, err := store.ReadManagedIntegrationFile(t.Context(), ManagedGitHubCredentialFile, true); err == nil {
						t.Fatal("failed integration mutation was not rolled back")
					}
				}
			})
		}
	}
}
