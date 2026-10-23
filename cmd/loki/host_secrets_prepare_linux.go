//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"loki/internal/config"
	"loki/internal/daemon"
	hostapplication "loki/internal/host/application"
	hostpolicy "loki/internal/host/policy"
	"loki/internal/platform/safeio"
	"loki/modules/execution/jobs"
)

// runHostPrepareSecrets is an installer/unit implementation command. It creates
// deployment metadata and empty directories, never a key or unlock credential.
func runHostPrepareSecrets(args []string, stdout, stderr io.Writer) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "service preparation requires the host administrator")
		return 1
	}
	flags := flag.NewFlagSet("host prepare-secrets", flag.ContinueOnError)
	flags.SetOutput(stderr)
	uid := flags.Uint64("uid", 0, "allocated dedicated broker UID")
	gid := flags.Uint64("gid", 0, "allocated dedicated broker GID")
	agent := flags.Uint64("agent-uid", 0, "installed agent UID")
	metadataGroup := flags.Uint64("metadata-gid", 0, "installed metadata socket group")
	jobDeployment := flags.String("job-deployment", "", "current owned OCI reservation; app identity remains stable")
	initialMaintenance := flags.String("initial-maintenance-operation", "", "original application write gate established before role sockets open")
	maintenance := flags.String("maintenance-operation", "", "original host operation inherited before launcher admission")
	interrupt := flags.Bool("interrupt-jobs", false, "persist the original explicit host interruption choice")
	stateOnly := flags.Bool("state-only", false, "prepare persistent generation metadata without runtime files")
	inspectIdentity := flags.Bool("inspect-identity", false, "inspect existing public selector metadata without preparing state")
	metadataOnly := flags.Bool("metadata-only", false, "validate existing identity and public policy without preparing files or protection")
	requireEmpty := flags.Bool("require-empty-generation", false, "require no existing app values/replay history before legacy migration; inspection only")
	identity := flags.String("identity", "/etc/loki-go/secrets-deployment.json", "protected host deployment metadata")
	state := flags.String("state", "/var/lib/loki-go/secrets", "host app-generation store")
	serviceState := flags.String("service-state", "/var/lib/loki-go/secrets", "app state path inside the service namespace")
	runtimeRoot := flags.String("runtime", "/run/loki-go/secrets", "tmpfs role directory")
	binary := flags.String("devtools", "/opt/loki/bin/devtools", "immutable provider program inside service namespace")
	configuration := flags.String("config", "/etc/loki-go/config.toml", "effective runtime configuration")
	github := flags.String("github-config", "", "public GitHub configuration fragment")
	contractPath := flags.String("execution-contract", "/usr/share/doc/loki/execution-contract.json", "effective execution contract")
	catalog := flags.String("catalog", "/etc/loki-go/application-resources.json", "host resource catalog")
	serviceCatalog := flags.String("service-catalog", "", "catalog path inside role namespaces; defaults to catalog")
	deploymentOwner := flags.String("owner", "", "explicit owned launcher namespace; defaults to identity path binding")
	unitDirectory := flags.String("unit-directory", "/etc/systemd/system/loki-go-secrets.service.d", "native generation binding drop-in directory; empty disables")
	overlayDirectory := flags.String("overlay-directory", "/etc/loki-go", "native role layouts to bind to this generation; empty disables")
	roleSource := flags.String("role-source", "", "immutable role layout source; defaults to overlay directory")
	candidateGeneration := flags.String("candidate-generation", "", "inactive generation owned by the original migration")
	candidateBefore := flags.String("candidate-before", "", "canonical public identity observed by the host owner")
	candidateOperation := flags.String("candidate-operation", "", "original migration operation UUID")
	commitCandidate := flags.Bool("commit-candidate", false, "publish the original host owner's verified candidate selector")
	candidatePolicy := flags.String("candidate-policy", "", "frozen target policy digest required for selector publication")
	restoreCandidate := flags.Bool("restore-candidate", false, "select only an original-operation sealed logical restore")
	policyBefore := flags.String("policy-before", "", "exact retained identity for original-operation policy replacement")
	policyLauncher := flags.String("policy-launcher-layout", "", "stopped original launcher layout for policy replacement")
	policyState := flags.String("policy-launcher-state", "", "original launcher state mount for policy replacement")
	policyStartup := flags.Bool("policy-startup", false, "bind policy only while the selected full reservation owns an idle stopped launcher journal")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return 2
	}
	if *inspectIdentity {
		if *policyBefore != "" || *policyLauncher != "" || *policyState != "" || *policyStartup {
			return 2
		}
		if *metadataOnly || *stateOnly || *candidateGeneration != "" || *candidateBefore != "" || *candidateOperation != "" || *commitCandidate || *candidatePolicy != "" || *restoreCandidate || *maintenance != "" || *initialMaintenance != "" || *interrupt {
			return 2
		}
		existing, err := hostapplication.ReadIdentity(*identity)
		if err != nil {
			fmt.Fprintln(stderr, "existing application identity is unavailable")
			return 1
		}
		if *requireEmpty {
			if err := hostapplication.RequireEmptyGeneration(*state, existing); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
		}
		if json.NewEncoder(stdout).Encode(existing) != nil {
			return 1
		}
		return 0
	}
	if *requireEmpty && !*metadataOnly {
		return 2
	}
	if *maintenance == "" {
		if *interrupt {
			return 2
		}
	} else if _, err := jobs.JobIDForRequestID(*maintenance); err != nil || *maintenance != strings.ToLower(*maintenance) {
		return 2
	}
	if *stateOnly && (*unitDirectory != "" || *overlayDirectory != "") {
		return 2
	}
	if *commitCandidate != (*candidatePolicy != "") || *restoreCandidate && !*commitCandidate {
		return 2
	}
	isCandidate := *candidateGeneration != "" || *candidateBefore != "" || *candidateOperation != "" || *commitCandidate
	if *metadataOnly && (isCandidate || *stateOnly || *maintenance != "" || *initialMaintenance != "" || *interrupt || *jobDeployment != "") {
		return 2
	}
	var before hostapplication.Identity
	if isCandidate {
		// A restore candidate establishes this same original operation's app
		// gate before opening its private role sockets. It cannot inherit a
		// different operation or change the gate during selector publication.
		if *candidateGeneration == "" || *candidateBefore == "" || *candidateOperation == "" || *stateOnly || *unitDirectory != "" || *overlayDirectory != "" || *maintenance != "" || *initialMaintenance != "" && (*commitCandidate || *initialMaintenance != *candidateOperation) || json.Unmarshal([]byte(*candidateBefore), &before) != nil {
			return 2
		}
		canonical, _ := json.Marshal(before)
		if string(canonical) != *candidateBefore {
			return 2
		}
	}
	if *serviceCatalog == "" {
		*serviceCatalog = *catalog
	}
	if *roleSource == "" {
		*roleSource = *overlayDirectory
	}
	for _, path := range []string{*overlayDirectory, *roleSource} {
		if path != "" && (!filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || strings.ContainsAny(path, "\x00\r\n")) {
			return 2
		}
	}
	if !filepath.IsAbs(*serviceCatalog) || filepath.Clean(*serviceCatalog) != *serviceCatalog || strings.ContainsAny(*serviceCatalog, "\x00\r\n") {
		return 2
	}
	if *deploymentOwner != "" {
		if len(*deploymentOwner) != 27 || !strings.HasPrefix(*deploymentOwner, "loki-tools-") || *deploymentOwner != strings.ToLower(*deploymentOwner) {
			return 2
		}
		if _, err := hex.DecodeString(strings.TrimPrefix(*deploymentOwner, "loki-tools-")); err != nil {
			return 2
		}
	}
	for _, id := range []uint64{*uid, *gid, *agent, *metadataGroup} {
		if id == 0 || id > uint64(^uint32(0)) {
			fmt.Fprintln(stderr, "service identities must be nonzero uint32 values")
			return 2
		}
	}
	c, err := config.LoadWithGitHub(*configuration, *github)
	if err != nil {
		fmt.Fprintln(stderr, "effective service configuration is unavailable")
		return 1
	}
	contract, err := loadExecutionContract(*contractPath)
	if err != nil {
		fmt.Fprintln(stderr, "execution contract is unavailable")
		return 1
	}
	resources, err := hostpolicy.LoadApplications(*catalog)
	if err != nil {
		fmt.Fprintln(stderr, "host resource catalog is unavailable")
		return 1
	}
	prepare := hostapplication.Prepare
	if *stateOnly {
		prepare = hostapplication.PrepareState
	}
	if isCandidate {
		prepare = func(options hostapplication.PrepareOptions) (hostapplication.Preparation, error) {
			options.MigrationOperation = *candidateOperation
			return hostapplication.PrepareCandidate(options, before, *candidateGeneration)
		}
	}
	options := hostapplication.PrepareOptions{UID: uint32(*uid), GID: uint32(*gid), AgentUID: uint32(*agent), MetadataGID: uint32(*metadataGroup), JobDeploymentID: *jobDeployment, IdentityFile: *identity, StateRoot: *state, ServiceStateRoot: *serviceState, RuntimeRoot: *runtimeRoot, Binary: *binary, Principal: "agent", Configuration: c, Contract: contract, Resources: resources}
	options.InitialMaintenanceOperation = *initialMaintenance
	if *policyStartup {
		if isCandidate || *metadataOnly || *stateOnly || *policyBefore != "" || *maintenance != "" || *jobDeployment == "" || *policyLauncher == "" || *policyState == "" {
			return 2
		}
		existing, err := hostapplication.ReadIdentity(*identity)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return 1
		}
		if err == nil && existing.Version == 2 {
			policy, err := hostapplication.PolicyDigest(options)
			if err != nil {
				return 1
			}
			if existing.PolicySHA256 != policy {
				journal, err := openApplicationPolicyJournal(*policyLauncher, *policyState, "")
				if err != nil {
					fmt.Fprintln(stderr, "selected full policy requires the idle original stopped launcher journal")
					return 1
				}
				defer journal.Close()
				if _, err := hostapplication.CommitPolicy(options, existing); err != nil {
					return 1
				}
			}
		}
	} else if *policyBefore != "" || *policyLauncher != "" || *policyState != "" {
		var expected hostapplication.Identity
		if isCandidate || *metadataOnly || *inspectIdentity || *stateOnly || *maintenance == "" || *policyBefore == "" || *policyLauncher == "" || *policyState == "" || len(*policyBefore) > 4096 || json.Unmarshal([]byte(*policyBefore), &expected) != nil {
			return 2
		}
		journal, err := openApplicationPolicyJournal(*policyLauncher, *policyState, *maintenance)
		if err != nil {
			fmt.Fprintln(stderr, "policy replacement requires the original stopped launcher maintenance journal")
			return 1
		}
		defer journal.Close()
		if _, err := hostapplication.CommitPolicy(options, expected); err != nil {
			fmt.Fprintln(stderr, "original application policy changed before publication")
			return 1
		}
	}
	if *metadataOnly {
		before, err := hostapplication.ReadIdentity(*identity)
		if err != nil || before.UID != options.UID || before.GID != options.GID || before.AgentUID != options.AgentUID || before.MetadataGID != options.MetadataGID || hostapplication.ValidateMetadata(options) != nil || hostapplication.RequireExistingGeneration(*state, before) != nil {
			fmt.Fprintln(stderr, "existing application identity or public policy is invalid")
			return 1
		}
		if *requireEmpty && hostapplication.RequireEmptyGeneration(*state, before) != nil {
			fmt.Fprintln(stderr, "existing application generation must be preserved through application backup/restore")
			return 1
		}
		policy, err := hostapplication.PolicyDigest(options)
		if err != nil {
			return 1
		}
		if json.NewEncoder(stdout).Encode(map[string]any{"identity": before, "policy": policy, "base_environment": contract.Environment}) != nil {
			return 1
		}
		return 0
	}
	if *commitCandidate {
		policy, err := hostpolicy.CompileApplications(c, contract, resources)
		if err != nil || policy.Digest() != *candidatePolicy {
			fmt.Fprintln(stderr, "candidate policy changed before selector publication")
			return 1
		}
		if err := hostapplication.RequireEmptyGeneration(*state, before); err != nil && !*restoreCandidate {
			fmt.Fprintln(stderr, err)
			return 1
		}
		options.MigrationOperation = *candidateOperation
		selectCandidate := hostapplication.CommitCandidate
		if *restoreCandidate {
			selectCandidate = hostapplication.CommitRestoredCandidate
		}
		selected, err := selectCandidate(options, before, *candidateGeneration)
		if err != nil || json.NewEncoder(stdout).Encode(selected) != nil {
			fmt.Fprintln(stderr, "candidate selector publication requires original host recovery")
			return 1
		}
		return 0
	}
	prepared, err := prepare(options)
	if err != nil {
		fmt.Fprintln(stderr, "secret service metadata or filesystem ownership requires recovery")
		return 1
	}
	if *unitDirectory != "" {
		if !filepath.IsAbs(*unitDirectory) || filepath.Clean(*unitDirectory) != *unitDirectory || strings.ContainsAny(prepared.HostGeneration+*serviceState+*unitDirectory, "\x00\r\n:%") {
			return 2
		}
		if err := os.Mkdir(*unitDirectory, 0755); err != nil && !os.IsExist(err) {
			return 1
		}
		// The generation selector stays host-only; the broker receives precisely
		// this generation at its fixed logical state path, without a symlink.
		// systemd parses ':' outside each quoted path. Quoting the entire
		// source:destination pair instead makes ':' part of one literal path.
		body := []byte("[Service]\nBindPaths=" + strconv.Quote(prepared.HostGeneration) + ":" + strconv.Quote(*serviceState) + "\n")
		path := filepath.Join(*unitDirectory, "20-generation.conf")
		if safeio.PublishPrivate(path, body, true) != nil || os.Chmod(path, 0644) != nil {
			fmt.Fprintln(stderr, "native generation binding could not be prepared")
			return 1
		}
	}
	if *overlayDirectory != "" {
		catalogBody, _ := json.Marshal(struct {
			Resources any `json:"resources"`
		}{Resources: prepared.Layout.Resources})
		if prepared.Layout.Resources == nil {
			catalogBody = []byte(`{"resources":[]}`)
		}
		catalogFile := filepath.Join(*overlayDirectory, "application-resources.json")
		if safeio.PublishPrivate(catalogFile, catalogBody, true) != nil || os.Chown(catalogFile, 0, int(*metadataGroup)) != nil || os.Chmod(catalogFile, 0640) != nil {
			fmt.Fprintln(stderr, "role resource catalog could not be prepared")
			return 1
		}
		owner := sha256.Sum256([]byte(filepath.Clean(*identity)))
		ownerName := "loki-tools-" + hex.EncodeToString(owner[:8])
		if *deploymentOwner != "" {
			ownerName = *deploymentOwner
		}
		jobDeployment := prepared.Layout.JobDeploymentID
		if jobDeployment == "" {
			jobDeployment = prepared.Identity.DeploymentID
		}
		common := map[string]any{"ApplicationCatalog": *serviceCatalog}
		changes := map[string]map[string]any{
			"mcp.json":      {"ApplicationCatalog": common["ApplicationCatalog"], "ApplicationSocket": prepared.Layout.MetadataSocket, "ApplicationUID": prepared.Identity.UID},
			"runtime.json":  {"ApplicationCatalog": common["ApplicationCatalog"], "ApplicationValuesExternal": true},
			"launcher.json": {"ApplicationCatalog": common["ApplicationCatalog"], "SecretSocket": prepared.Layout.Socket, "SecretBrokerUID": prepared.Identity.UID, "SecretPrincipal": "agent", "DeploymentID": jobDeployment, "DeploymentOwner": ownerName, "InitialMaintenanceOperation": *maintenance, "InitialMaintenanceInterrupt": *interrupt},
		}
		for name, fields := range changes {
			path := filepath.Join(*overlayDirectory, name)
			var layout map[string]any
			if daemon.ReadJSON(filepath.Join(*roleSource, name), &layout) != nil {
				fmt.Fprintln(stderr, "native role layout is unavailable")
				return 1
			}
			for key, value := range fields {
				layout[key] = value
			}
			raw, _ := json.Marshal(layout)
			if safeio.PublishPrivate(path, raw, true) != nil || os.Chown(path, 0, int(*metadataGroup)) != nil || os.Chmod(path, 0640) != nil {
				fmt.Fprintln(stderr, "native application role binding could not be prepared")
				return 1
			}
		}
	}
	result := map[string]any{"prepared": true, "policy": prepared.Layout.PolicySHA256, "broker_uid": prepared.Identity.UID, "broker_gid": prepared.Identity.GID, "deployment_id": prepared.Identity.DeploymentID, "runner_id": prepared.Identity.RunnerID, "active_generation": prepared.Identity.ActiveGeneration}
	if isCandidate {
		result["candidate"] = true
	}
	if json.NewEncoder(stdout).Encode(result) != nil {
		return 1
	}
	return 0
}
