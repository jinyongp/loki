package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	windowshost "loki/internal/host/windows"
)

const (
	localConnectionID   = windowshost.LocalConnectionID
	localConnectionKind = windowshost.LocalConnectionKind
)

type localConnectionView struct {
	LocalOrigin     string
	TokenFile       string
	HostState       string
	Release         string
	UpdatePrepared  bool
	UpdateAvailable bool
}

type connectionListItem struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	DisplayName string   `json:"display_name"`
	Description string   `json:"description"`
	State       string   `json:"state"`
	Configured  bool     `json:"configured"`
	Enabled     bool     `json:"enabled"`
	Actions     []string `json:"actions,omitempty"`
}

type connectionListPayload struct {
	SchemaVersion int                  `json:"schema_version"`
	Distribution  string               `json:"distribution"`
	Connections   []connectionListItem `json:"connections"`
}

type managedConnectionDetail struct {
	Descriptor windowshost.ConnectionProviderDescriptor `json:"descriptor"`
	Status     windowshost.ManagedConnectionStatus      `json:"status"`
}

type connectionShowPayload struct {
	SchemaVersion int                      `json:"schema_version"`
	Distribution  string                   `json:"distribution"`
	ID            string                   `json:"id"`
	Kind          string                   `json:"kind"`
	Local         *localConnectionJSON     `json:"local,omitempty"`
	Managed       *managedConnectionDetail `json:"managed,omitempty"`
}

type localConnectionJSON struct {
	DisplayName     string `json:"display_name"`
	Description     string `json:"description"`
	LocalOrigin     string `json:"local_origin"`
	Authentication  string `json:"authentication"`
	TokenFile       string `json:"token_file"`
	State           string `json:"state"`
	Release         string `json:"release,omitempty"`
	UpdatePrepared  bool   `json:"update_prepared"`
	UpdateAvailable bool   `json:"update_available"`
}

func buildConnectionList(
	localPresent bool,
	descriptors []windowshost.ConnectionProviderDescriptor,
	states []windowshost.ConnectionState,
) ([]connectionListItem, error) {
	stateByProvider := make(map[string]windowshost.ConnectionState, len(states))
	for _, state := range states {
		if strings.TrimSpace(state.Provider) == "" {
			return nil, errors.New("managed connection state has no provider")
		}
		if _, duplicate := stateByProvider[state.Provider]; duplicate {
			return nil, fmt.Errorf("managed connection provider %q has duplicate state", state.Provider)
		}
		stateByProvider[state.Provider] = state
	}

	localState := "unavailable"
	if localPresent {
		localState = "available"
	}
	items := []connectionListItem{{
		ID:          localConnectionID,
		Kind:        localConnectionKind,
		DisplayName: "Local MCP",
		Description: "Loopback MCP endpoint exposed by the local Loki appliance.",
		State:       localState,
		Configured:  localPresent,
		Enabled:     localPresent,
		Actions:     []string{"show"},
	}}
	for _, descriptor := range descriptors {
		state, configured := stateByProvider[descriptor.ID]
		connectionState := "not-configured"
		enabled := false
		if configured {
			enabled = state.Enabled
			if enabled {
				connectionState = "enabled"
			} else {
				connectionState = "stopped"
			}
		}
		items = append(items, connectionListItem{
			ID:          descriptor.ID,
			Kind:        descriptor.Kind,
			DisplayName: descriptor.DisplayName,
			Description: descriptor.Description,
			State:       connectionState,
			Configured:  configured,
			Enabled:     enabled,
			Actions:     append([]string(nil), descriptor.Actions...),
		})
	}
	slices.SortFunc(items[1:], func(left, right connectionListItem) int {
		return strings.Compare(left.ID, right.ID)
	})
	return items, nil
}

func renderConnectionList(distribution string, items []connectionListItem, stdout io.Writer) {
	fmt.Fprintf(stdout, "Loki connections (%s)\n", distribution)
	for _, item := range items {
		fmt.Fprintf(stdout, "  %-8s %-8s %-14s %s\n",
			item.ID, item.Kind, item.State, item.DisplayName)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Use 'loki connection show NAME' for details.")
	fmt.Fprintln(stdout, "Use 'loki connection setup PROVIDER' to configure a managed connection.")
}

func writeConnectionListJSON(distribution string, items []connectionListItem, stdout io.Writer) error {
	return json.NewEncoder(stdout).Encode(connectionListPayload{
		SchemaVersion: 1,
		Distribution:  distribution,
		Connections:   items,
	})
}

func renderLocalConnection(distribution string, local localConnectionView, stdout io.Writer) {
	fmt.Fprintln(stdout, "Local MCP connection")
	fmt.Fprintf(stdout, "  Distribution: %s\n", distribution)
	fmt.Fprintf(stdout, "  Local origin: %s\n", local.LocalOrigin)
	fmt.Fprintln(stdout, "  Authentication: bearer token file")
	fmt.Fprintf(stdout, "  Token file: %s\n", local.TokenFile)
	if local.Release != "" {
		fmt.Fprintf(stdout, "  Appliance release: v%s\n", strings.TrimPrefix(local.Release, "v"))
	}
	fmt.Fprintf(stdout, "  Host state: %s\n", local.HostState)
	fmt.Fprintln(stdout, "  Reachability: loopback only")
}

func writeLocalConnectionJSON(distribution string, local localConnectionView, stdout io.Writer) error {
	return json.NewEncoder(stdout).Encode(connectionShowPayload{
		SchemaVersion: 1,
		Distribution:  distribution,
		ID:            localConnectionID,
		Kind:          localConnectionKind,
		Local: &localConnectionJSON{
			DisplayName:     "Local MCP",
			Description:     "Loopback MCP endpoint exposed by the local Loki appliance.",
			LocalOrigin:     local.LocalOrigin,
			Authentication:  "bearer-token-file",
			TokenFile:       local.TokenFile,
			State:           local.HostState,
			Release:         local.Release,
			UpdatePrepared:  local.UpdatePrepared,
			UpdateAvailable: local.UpdateAvailable,
		},
	})
}

func renderManagedConnection(
	distribution string,
	descriptor windowshost.ConnectionProviderDescriptor,
	status windowshost.ManagedConnectionStatus,
	stdout io.Writer,
) {
	fmt.Fprintf(stdout, "%s (%s)\n", descriptor.DisplayName, descriptor.ID)
	fmt.Fprintf(stdout, "  Distribution: %s\n", distribution)
	fmt.Fprintf(stdout, "  Type: %s\n", descriptor.Kind)
	fmt.Fprintf(stdout, "  Description: %s\n", descriptor.Description)
	if !status.Configured {
		fmt.Fprintln(stdout, "  State: not configured")
		fmt.Fprintf(stdout, "  Setup: loki connection setup %s\n", descriptor.ID)
		return
	}
	fmt.Fprintf(stdout, "  State: %s\n", status.Runtime.State)
	fmt.Fprintf(stdout, "  Enabled: %t\n", status.State.Enabled)
	fmt.Fprintf(stdout, "  Healthy: %t\n", status.Runtime.Healthy)
	fmt.Fprintf(stdout, "  Ready: %t\n", status.Runtime.Ready)
	if status.Runtime.Detail != "" {
		fmt.Fprintf(stdout, "  Detail: %s\n", status.Runtime.Detail)
	}
}

func writeManagedConnectionJSON(
	distribution string,
	descriptor windowshost.ConnectionProviderDescriptor,
	status windowshost.ManagedConnectionStatus,
	stdout io.Writer,
) error {
	return json.NewEncoder(stdout).Encode(connectionShowPayload{
		SchemaVersion: 1,
		Distribution:  distribution,
		ID:            descriptor.ID,
		Kind:          descriptor.Kind,
		Managed: &managedConnectionDetail{
			Descriptor: descriptor,
			Status:     status,
		},
	})
}
