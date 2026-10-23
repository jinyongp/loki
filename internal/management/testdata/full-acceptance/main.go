//go:build linux

// Fixture-only administrator for the receipt-bound full composition acceptance.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"loki/internal/admin"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	hostpolicy "loki/internal/host/policy"
	"loki/internal/management"
)

func run() error {
	if len(os.Args) != 3 || os.Geteuid() != 0 {
		return fmt.Errorf("fixture administrator requires root and action/root arguments")
	}
	store := management.Store{Root: os.Args[2]}
	switch os.Args[1] {
	case "grant":
		r := controlpolicy.ApplicationResource{
			ID: "3c775d16-91dd-4eb9-9819-5c9359e421f0", Alias: "fixture", Repository: "repo:fixture",
			Principals:   []controlpolicy.ApplicationGrant{{Principal: "agent", Metadata: true, Write: true}},
			Environments: []string{"common"}, Keys: map[string]string{"FIXTURE_TOKEN": "secret"}, Network: "none",
		}
		plan, err := store.PrepareApplicationResources(hostpolicy.ApplicationChange{Action: "set", Alias: r.Alias, Resource: &r})
		if err != nil {
			return err
		}
		return store.ApplyApplicationResources(context.Background(), plan)
	case "init":
		topology, err := store.FullTopology()
		if err != nil {
			return err
		}
		for _, service := range topology.Services {
			if service.Name != "secrets" {
				continue
			}
			for _, mount := range service.Mounts {
				if mount.Target == "/run/loki/secrets" && mount.Kind == "bind" {
					client := admin.ApplicationClient{Socket: filepath.Join(mount.Source, "admin", "control.sock"), ExpectedUID: service.UID}
					body := []byte("synthetic-full-acceptance-protection")
					defer clear(body)
					_, err := client.Call(context.Background(), appvalues.AdminRequest{Action: "protection_init"}, body)
					return err
				}
			}
		}
		return fmt.Errorf("selected secret administrator socket is absent")
	default:
		return fmt.Errorf("unknown fixture action")
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
