package runtime

import (
	"context"

	"loki/internal/audit"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/rpc"
)

func AuditOperations(log *audit.Log) map[string]rpc.Operation {
	return map[string]rpc.Operation{"audit": {Grant: controlpolicy.Agent, Handle: runtimeTyped(func(_ context.Context, r pageInput) (map[string]any, error) {
		return log.ReadPage(r.Offset, r.Limit)
	})}}
}

func AuditSink(log *audit.Log, onError func(error)) func(rpc.Event) {
	return func(e rpc.Event) {
		if err := log.Runtime(e.Operation, e.UID, e.Success, e.Profile); err != nil && onError != nil {
			onError(err)
		}
	}
}
