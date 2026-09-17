package portguard

import "errors"

var ErrProtected = errors.New("protected Loki service port")

type Policy struct {
	protected map[int]struct{}
}

func ValidateNumber(port int) error {
	if port < 1024 || port > 65535 {
		return errors.New("port must be an integer between 1024 and 65535")
	}
	return nil
}

func NewPolicy(ports ...int) (Policy, error) {
	policy := Policy{protected: make(map[int]struct{}, len(ports))}
	for _, port := range ports {
		if err := ValidateNumber(port); err != nil {
			return Policy{}, err
		}
		policy.protected[port] = struct{}{}
	}
	return policy, nil
}

func (p Policy) Validate(port int) error {
	if err := ValidateNumber(port); err != nil {
		return err
	}
	if _, protected := p.protected[port]; protected {
		return ErrProtected
	}
	return nil
}
