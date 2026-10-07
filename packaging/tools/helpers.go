package tools

import _ "embed"

// Helpers is the exact upstream helper catalog bound into the native manager.
// Each download still verifies the pinned upstream length and SHA-256.
//go:embed connect-helpers.json
var Helpers []byte
