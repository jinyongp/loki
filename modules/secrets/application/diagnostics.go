//go:build linux

package application

import "loki/internal/fault"

// ProviderDiagnostic shares the reviewed error policy with private Job
// operators. Provider messages, paths and executable remedies are discarded.
func ProviderDiagnostic(err error) fault.Detail {
	return fault.Describe(safeError(err))
}
