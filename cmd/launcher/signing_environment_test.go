package main

import (
	"testing"

	"loki/internal/platform/sandbox"
)

func TestLauncherSigningDeploymentDefaults(t *testing.T) {
	for _, fixture := range []struct {
		name           string
		env            map[string]string
		valid, enabled bool
	}{
		{name: "unset", env: map[string]string{}, valid: true},
		{name: "disabled-compose-placeholders", env: map[string]string{
			"LOKI_SIGNING_PUBLIC_KEY_FILE": "/dev/null", "LOKI_SIGNING_GIT_CONFIG_FILE": "/dev/null", "LOKI_SIGNING_ALLOWED_SIGNERS_FILE": "/dev/null",
		}, valid: true},
		{name: "enabled", env: map[string]string{
			"LOKI_SIGNING_SOCKET_VOLUME":        "loki_signing-socket",
			"LOKI_SIGNING_PUBLIC_KEY_FILE":      "/var/lib/loki/public/key.pub",
			"LOKI_SIGNING_GIT_CONFIG_FILE":      "/var/lib/loki/public/signing.gitconfig",
			"LOKI_SIGNING_ALLOWED_SIGNERS_FILE": "/var/lib/loki/public/allowed_signers",
		}, valid: true, enabled: true},
		{name: "partial", env: map[string]string{"LOKI_SIGNING_SOCKET_VOLUME": "loki_signing-socket"}},
		{name: "missing-socket", env: map[string]string{"LOKI_SIGNING_PUBLIC_KEY_FILE": "/var/lib/loki/public/key.pub"}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			options := launcherSigningDefaults(func(name string) string { return fixture.env[name] })
			if fixture.valid && !fixture.enabled && options != (sandbox.SigningPolicyOptions{}) {
				t.Fatalf("disabled signing acquired authority: %#v", options)
			}
			layout := validLauncherLayout(t)
			layout.SigningSocketVolume = options.SocketVolume
			layout.SigningPublicKey = options.PublicKey
			layout.SigningGitConfig = options.GitConfig
			layout.SigningAllowedSigners = options.AllowedSigners
			_, err := buildLauncher(layout)
			if (err == nil) != fixture.valid {
				t.Fatalf("valid=%t err=%v", fixture.valid, err)
			}
		})
	}
}
