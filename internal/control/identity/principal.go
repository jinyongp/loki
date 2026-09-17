package identity

type Kind uint8

const (
	Unknown Kind = iota
	Agent
	HostAdministrator
)

type Principal struct {
	Kind     Kind
	PID      int32
	UID, GID uint32
}

type Resolver interface {
	Resolve(pid int32, uid, gid uint32) Principal
}

type ResolverFunc func(pid int32, uid, gid uint32) Principal

func (f ResolverFunc) Resolve(pid int32, uid, gid uint32) Principal {
	return f(pid, uid, gid)
}

type UnixResolver struct {
	AgentUID uint32
}

func (r UnixResolver) Resolve(pid int32, uid, gid uint32) Principal {
	kind := Unknown
	switch {
	case uid == 0:
		kind = HostAdministrator
	case uid == r.AgentUID:
		kind = Agent
	}
	return Principal{Kind: kind, PID: pid, UID: uid, GID: gid}
}
