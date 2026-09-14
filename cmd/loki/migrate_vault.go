package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"loki/internal/secret"
	"loki/internal/state"
)

type migrationMarker struct {
	SourceSHA256 string `json:"source_sha256"`
}

func cleanAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

func readMigrationMarker(directory string) (migrationMarker, error) {
	var marker migrationMarker
	data, err := os.ReadFile(filepath.Join(directory, "migration.json"))
	if err != nil {
		return marker, err
	}
	if json.Unmarshal(data, &marker) != nil {
		return marker, errors.New("migration marker is invalid")
	}
	decoded, decodeErr := hex.DecodeString(marker.SourceSHA256)
	if decodeErr != nil || len(decoded) != 32 {
		return marker, errors.New("migration marker is invalid")
	}
	return marker, nil
}

func runMigrateVault(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: loki migrate-vault import|restore [OPTIONS]")
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	var result map[string]any
	var err error
	switch args[0] {
	case "import":
		flags := flag.NewFlagSet("migrate-vault import", flag.ContinueOnError)
		flags.SetOutput(stderr)
		source := flags.String("source-copy", "", "private offline copy of the Python v1 vault")
		destination := flags.String("destination", "", "new Go vault directory")
		if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || !cleanAbsolute(*source) || !cleanAbsolute(*destination) {
			return 2
		}
		if filepath.Clean(*source) == "/var/lib/loki/runtime" {
			fmt.Fprintln(stderr, "loki: source-copy must not be the live Python vault")
			return 1
		}
		_, statErr := os.Lstat(*destination)
		reused := statErr == nil
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			err = statErr
		} else {
			err = state.ImportLegacyWithTransform(ctx, *source, *destination, secret.Validate, secret.MigrateLegacyDocument)
		}
		if err == nil {
			var marker migrationMarker
			marker, err = readMigrationMarker(*destination)
			if err == nil {
				var snapshot state.Snapshot
				snapshot, err = (state.Store{Dir: *destination, Validate: secret.Validate}).Load(ctx)
				if err == nil {
					var document struct {
						Profiles map[string]struct {
							Secrets map[string]string `json:"secrets"`
						} `json:"profiles"`
					}
					decoder := json.NewDecoder(bytes.NewReader(snapshot.Data))
					err = decoder.Decode(&document)
					count := 0
					for _, profile := range document.Profiles {
						count += len(profile.Secrets)
					}
					result = map[string]any{"migrated": true, "reused": reused, "source_fingerprint": marker.SourceSHA256, "profiles": len(document.Profiles), "secrets": count, "target_revision": snapshot.Revision}
				}
			}
		}
	case "restore":
		flags := flag.NewFlagSet("migrate-vault restore", flag.ContinueOnError)
		flags.SetOutput(stderr)
		migration := flags.String("migration", "", "Go migration directory with embedded legacy backup")
		destination := flags.String("destination", "", "new Python v1 restore directory")
		if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || !cleanAbsolute(*migration) || !cleanAbsolute(*destination) {
			return 2
		}
		err = state.RestoreLegacy(ctx, *migration, *destination)
		if err == nil {
			var marker migrationMarker
			marker, err = readMigrationMarker(*migration)
			result = map[string]any{"restored": true, "source_fingerprint": marker.SourceSHA256}
		}
	default:
		fmt.Fprintln(stderr, "usage: loki migrate-vault import|restore [OPTIONS]")
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "loki:", err)
		return 1
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if encoder.Encode(result) != nil {
		return 1
	}
	return 0
}
