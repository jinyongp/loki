package policy

import "loki/internal/control/identity"

type Grant uint8

const (
	Agent Grant = iota + 1
	HostAdministration
)

func Allows(principal identity.Principal, grant Grant) bool {
	switch principal.Kind {
	case identity.HostAdministrator:
		return grant == Agent || grant == HostAdministration
	case identity.Agent:
		return grant == Agent
	default:
		return false
	}
}
