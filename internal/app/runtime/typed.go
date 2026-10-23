package runtime

import (
	"context"
	"encoding/json"

	"loki/internal/rpc"
)

type pageInput struct {
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
}

func runtimeTyped[T any](handler func(context.Context, T) (map[string]any, error)) rpc.Handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		request, err := rpc.Decode[T](raw)
		if err != nil {
			return nil, err
		}
		return handler(ctx, request)
	}
}
