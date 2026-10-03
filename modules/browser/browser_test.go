package browser

import "testing"

func TestUnsafeServerToolsRequireOptIn(t *testing.T) {
	for _, name := range []string{"browser_run_code", "browser_run_code_unsafe", "execute"} {
		if Allowed(name, nil) {
			t.Fatalf("server execution exposed by default: %s", name)
		}
		if !Allowed(name, []string{"unsafe-code"}) {
			t.Fatalf("explicit capability rejected: %s", name)
		}
	}
	if !Allowed("browser_evaluate", nil) {
		t.Fatal("ordinary page evaluation suppressed")
	}
}
