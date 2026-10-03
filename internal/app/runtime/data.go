package runtime

import "path/filepath"

// Composition supplies separate owner mounts in full mode. The default paths
// keep private worker fixtures usable; they are not a public migration API.
func runtimeDataDirectories(o RuntimeOptions) (secrets, github, coordination string) {
	secrets = o.SecretStateDirectory
	if secrets == "" {
		secrets = o.StateDirectory
	}
	github = o.GitHubStateDirectory
	if github == "" {
		github = filepath.Join(o.StateDirectory, "providers", "github")
	}
	coordination = o.CoordinationStateDirectory
	if coordination == "" {
		coordination = filepath.Join(o.StateDirectory, "context")
	}
	return
}
