package devtools

import (
	"context"
	"encoding/json"
	"errors"
)

// Broker retains a rejecting boundary for older clients during the contract
// transition. Coordination never resolves app values or launches processes.
type Broker struct{ Client *Client }

func (Broker) Call(context.Context, string, json.RawMessage, string, []string) (json.RawMessage, error) {
	return nil, errors.New("devtools process secret delivery is unavailable; use a protected Job")
}
