package service

import (
	"context"

	"loki/internal/audit"
	"loki/internal/rpc"
)

func AuditOperations(log *audit.Log) map[string]rpc.Operation {
	return map[string]rpc.Operation{"audit": {Permission: rpc.Agent, Handle: runtimeTyped(func(ctx context.Context, r struct{ Limit *int }) (map[string]any, error) {
		limit := 50
		if r.Limit != nil {
			limit = *r.Limit
		}
		return log.Read(limit)
	})}}
}

func AuditSink(log *audit.Log, onError func(error)) func(rpc.Event) {
	return func(e rpc.Event) {
		if err := log.Runtime(e.Operation, e.UID, e.Success, e.Profile); err != nil && onError != nil {
			onError(err)
		}
	}
}
