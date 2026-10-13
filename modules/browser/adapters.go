package browser

import (
	_ "embed"
	"os"
	"path/filepath"
)

//go:embed adapters/devtools.mjs
var devtoolsAdapter []byte

//go:embed adapters/playwright.cjs
var playwrightAdapter []byte

func writeEngineAdapter(profile, name string, source []byte) (string, error) {
	path := filepath.Join(profile, name)
	if err := os.WriteFile(path, source, 0600); err != nil {
		return "", err
	}
	return path, nil
}
