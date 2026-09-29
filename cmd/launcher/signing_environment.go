package main

import (
	"strings"

	"loki/internal/platform/sandbox"
)

// Signing defaults are deployment-owned environment values, not workload
// input. Older release launchers ignore these additive values during rollback.
func launcherSigningDefaults(getenv func(string) string) sandbox.SigningPolicyOptions {
	options := sandbox.SigningPolicyOptions{
		SocketVolume:   strings.TrimSpace(getenv("LOKI_SIGNING_SOCKET_VOLUME")),
		PublicKey:      strings.TrimSpace(getenv("LOKI_SIGNING_PUBLIC_KEY_FILE")),
		GitConfig:      strings.TrimSpace(getenv("LOKI_SIGNING_GIT_CONFIG_FILE")),
		AllowedSigners: strings.TrimSpace(getenv("LOKI_SIGNING_ALLOWED_SIGNERS_FILE")),
	}
	if options.SocketVolume == "" {
		// Compose requires a source for optional config mounts. /dev/null is
		// that disabled placeholder, never a signing-authority source.
		for _, path := range []*string{&options.PublicKey, &options.GitConfig, &options.AllowedSigners} {
			if *path == "/dev/null" {
				*path = ""
			}
		}
	}
	return options
}
