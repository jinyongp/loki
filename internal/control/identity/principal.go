package identity

type Kind uint8

const (
	Unknown Kind = iota
	Agent
	HostAdministrator
	Executor
	PortInspector
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
	AgentUID         uint32
	PortInspectorUID *uint32
}

func (r UnixResolver) Resolve(pid int32, uid, gid uint32) Principal {
	kind := Unknown
	switch {
	case uid == 0:
		kind = HostAdministrator
	case uid == r.AgentUID:
		kind = Agent
	case r.PortInspectorUID != nil && *r.PortInspectorUID != 0 && *r.PortInspectorUID != r.AgentUID && uid == *r.PortInspectorUID:
		kind = PortInspector
	}
	return Principal{Kind: kind, PID: pid, UID: uid, GID: gid}
}

// FixedUIDResolver recognizes root plus exactly one configured non-root role.
type FixedUIDResolver struct {
	UID  uint32
	Kind Kind
}

func (r FixedUIDResolver) Valid() bool {
	return r.UID != 0 && (r.Kind == Agent || r.Kind == Executor)
}

func (r FixedUIDResolver) Resolve(pid int32, uid, gid uint32) Principal {
	kind := Unknown
	if r.Valid() {
		switch {
		case uid == 0:
			kind = HostAdministrator
		case uid == r.UID:
			kind = r.Kind
		}
	}
	return Principal{Kind: kind, PID: pid, UID: uid, GID: gid}
}
