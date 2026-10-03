package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"loki/internal/config"
	"loki/internal/rpc"
)

func gateOperations(gate *config.ToolGate, module string, operations map[string]rpc.Operation) map[string]rpc.Operation {
	if gate == nil {
		return operations
	}
	for name, operation := range operations {
		base := operation.Handle
		operation.Handle = func(ctx context.Context, data json.RawMessage) (any, error) {
			var err error
			switch module {
			case "ports":
				err = fmt.Errorf("port inspection has no active private owner")
				owners := []string{"browser", "execution", "sharing"}
				if name == "stop" {
					owners = []string{"execution", "sharing"}
				}
				for _, owner := range owners {
					if gate.Resource(owner) == nil {
						err = nil
						break
					}
				}
			case "workloads":
				err = gate.Resource("execution")
			case "personal-projects":
				choice, selectionErr := gate.Selection("github")
				err = selectionErr
				if err == nil && !slices.Contains(choice.Capabilities, "personal-projects") {
					err = fmt.Errorf("GitHub personal-projects capability is disabled")
				}
			default:
				_, err = gate.Selection(module)
			}
			if err != nil {
				return nil, err
			}
			return base(ctx, data)
		}
		operations[name] = operation
	}
	return operations
}
