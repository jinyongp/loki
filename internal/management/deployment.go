package management

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"

	"loki/internal/tools"
)

var deploymentIDPattern = regexp.MustCompile(`^deployment-[a-f0-9]{32}$`)
var deploymentDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// DeploymentReservation persists before any service is created. It protects
// immutable generations even after the management command exits or crashes.
// Both an old deployment and a prepared replacement stay protected until the
// backend confirms that all resources of the respective deployment are absent.
type DeploymentReservation struct {
	ID         string           `json:"id"`
	Release    string           `json:"release"`
	PlanDigest string           `json:"plan_digest"`
	Artifacts  []tools.Artifact `json:"artifacts"`
	Services   []string         `json:"services"`
}

type deploymentRegistry struct {
	Schema      int                     `json:"schema"`
	Deployments []DeploymentReservation `json:"deployments"`
}

func (r deploymentRegistry) validate() error {
	if r.Schema != 1 || r.Deployments == nil || len(r.Deployments) > 128 {
		return fmt.Errorf("invalid deployment reservation registry")
	}
	seen := map[string]bool{}
	for _, deployment := range r.Deployments {
		if err := (tools.Config{Schema: 1, Release: deployment.Release, Host: tools.Host{Kind: "local"}, Mode: tools.Full}).Validate(); err != nil {
			return err
		}
		if !deploymentIDPattern.MatchString(deployment.ID) || !deploymentDigestPattern.MatchString(deployment.PlanDigest) || seen[deployment.ID] || len(deployment.Artifacts) == 0 || len(deployment.Services) == 0 {
			return fmt.Errorf("invalid deployment reservation")
		}
		seen[deployment.ID] = true
		modules := map[tools.ID]bool{}
		for _, artifact := range deployment.Artifacts {
			if err := artifact.Validate(); err != nil {
				return err
			}
			if artifact.Target.Mode != tools.Full || modules[artifact.Module] {
				return fmt.Errorf("deployment generation identity mismatch")
			}
			modules[artifact.Module] = true
		}
		services := map[string]bool{}
		for _, service := range deployment.Services {
			if service != "mcp" && service != "runtime" && service != "executor" && service != "launcher" && service != "egress" && service != "browser" && service != "browser-proxy" && service != "git-signing" {
				return fmt.Errorf("invalid reserved service")
			}
			if services[service] {
				return fmt.Errorf("duplicate reserved service")
			}
			services[service] = true
		}
	}
	return nil
}

func (s Store) deploymentRegistry() (deploymentRegistry, error) {
	r := deploymentRegistry{Schema: 1, Deployments: []DeploymentReservation{}}
	err := readOwnedJSON(filepath.Join(s.Root, "deployments.json"), tools.MaxManifestBytes, &r)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	return r, r.validate()
}

func (s Store) Deployments() ([]DeploymentReservation, error) {
	r, err := s.deploymentRegistry()
	return r.Deployments, err
}

// ReserveDeployment recomputes the plan under the host mutation lock. A stale
// caller cannot reserve resources from a replaced or disabled composition.
func (s Store) ReserveDeployment(expected FullPlan) (DeploymentReservation, error) {
	unlock, err := s.Lock()
	if err != nil {
		return DeploymentReservation{}, err
	}
	defer unlock()
	if err := s.RequireMutable(); err != nil {
		return DeploymentReservation{}, err
	}
	plan, err := s.PlanFull()
	if err != nil {
		return DeploymentReservation{}, err
	}
	if !reflect.DeepEqual(plan, expected) || len(plan.Services) == 0 {
		return DeploymentReservation{}, fmt.Errorf("deployment composition changed or has no enabled services")
	}
	state, err := s.Load()
	if err != nil {
		return DeploymentReservation{}, err
	}
	r, err := s.deploymentRegistry()
	if err != nil {
		return DeploymentReservation{}, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return DeploymentReservation{}, err
	}
	reservation := DeploymentReservation{ID: "deployment-" + hex.EncodeToString(random[:]), Release: plan.Release, PlanDigest: fullPlanDigest(plan), Artifacts: []tools.Artifact{}, Services: []string{}}
	for _, program := range plan.Programs {
		artifact := state.Installed[program.Module].Artifact
		if artifact.Identity() != program.Identity {
			return DeploymentReservation{}, fmt.Errorf("deployment program changed before reservation")
		}
		reservation.Artifacts = append(reservation.Artifacts, artifact)
	}
	for _, service := range plan.Services {
		reservation.Services = append(reservation.Services, service.Name)
	}
	r.Deployments = append(r.Deployments, reservation)
	if err := r.validate(); err != nil {
		return DeploymentReservation{}, err
	}
	if err := atomicJSON(filepath.Join(s.Root, "deployments.json"), r); err != nil {
		return DeploymentReservation{}, err
	}
	return reservation, nil
}

func fullPlanDigest(plan FullPlan) string {
	data, _ := json.Marshal(plan)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// DeploymentObserver must check the backend's owned containers, processes and
// other resources, including prepared resources after an interrupted start.
// A connection failure or unknown ownership is not evidence of termination.
type DeploymentObserver interface {
	ConfirmAbsent(context.Context, DeploymentReservation) error
}

func (s Store) ReleaseDeployment(ctx context.Context, id string, observer DeploymentObserver) error {
	if !deploymentIDPattern.MatchString(id) || observer == nil {
		return fmt.Errorf("deployment release requires its owned backend observer")
	}
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	r, err := s.deploymentRegistry()
	if err != nil {
		return err
	}
	index := slices.IndexFunc(r.Deployments, func(v DeploymentReservation) bool { return v.ID == id })
	if index < 0 {
		return nil
	}
	if err := observer.ConfirmAbsent(ctx, r.Deployments[index]); err != nil {
		return fmt.Errorf("deployment resources remain protected: %w", err)
	}
	r.Deployments = slices.Delete(r.Deployments, index, index+1)
	return atomicJSON(filepath.Join(s.Root, "deployments.json"), r)
}

func (s Store) requireGenerationNotDeployed(artifact tools.Artifact) error {
	r, err := s.deploymentRegistry()
	if err != nil {
		return err
	}
	for _, deployment := range r.Deployments {
		for _, used := range deployment.Artifacts {
			if used.Identity() == artifact.Identity() {
				return fmt.Errorf("generation is reserved by %s; stop its owned services before removal: %w", deployment.ID, ErrGenerationInUse)
			}
		}
	}
	return nil
}
