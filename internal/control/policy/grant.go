package policy

import "loki/internal/control/identity"

type Grant uint8

const (
	Agent              Grant = 1
	HostAdministration Grant = 2
	WorkloadLaunch     Grant = 3
)

func Allows(principal identity.Principal, grant Grant) bool {
	switch principal.Kind {
	case identity.HostAdministrator:
		return grant == Agent || grant == HostAdministration || grant == WorkloadLaunch
	case identity.Agent:
		return grant == Agent
	case identity.Executor:
		return grant == WorkloadLaunch
	default:
		return false
	}
}
