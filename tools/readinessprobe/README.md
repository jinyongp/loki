# Go readiness baseline probe

`go run ./tools/readinessprobe` reproduces the remaining synthetic observations recorded in `docs/go-readiness-review.md` for R1, R3, R5, R7, R11, and R14. R4 is covered by permanent managed-credential regressions in `internal/secret`, `internal/service`, and `internal/devtools`; R8/R9 are covered by permanent regressions in `internal/toolchain/install_test.go` and `internal/toolchain/archive_safety_test.go`.

The command is deliberately **non-gating**. Exit status zero means every isolated observation completed; it does not mean the product is correct and it does not assert that the current broken behavior should remain. The JSON booleans are expected to change as fixes land. Each scenario must be converted into a positive regression assertion in the work item that fixes the corresponding defect, after which the redundant baseline observation can be removed.

The probe uses temporary Git repositories, temporary encrypted state with synthetic values, in-memory MCP transports, and short-lived child processes. It does not read the repository index being worked on, Loki runtime sockets, deployed credentials, or host deployment state. The detached-child scenario explicitly kills its synthetic child after observing whether timeout cleanup owned it.
