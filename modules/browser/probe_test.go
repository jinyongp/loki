package browser

import (
	"slices"
	"testing"
)

func TestProbeRequiresExactRunningBrowserVersion(t *testing.T) {
	for _, output := range []string{
		`{"version":"154.0.8037.9"}`,
		`{"version":"155.0.8059.39","ready":true}`,
		`{"version":"155.0.8059.39"} {}`,
		`Google Chrome for Testing 155.0.8059.39`,
	} {
		if err := checkProbeVersion([]byte(output)); err == nil {
			t.Fatalf("accepted unusable startup response: %s", output)
		}
	}
	if err := checkProbeVersion([]byte(`{"version":"155.0.8059.39"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeEnvironmentKeepsBundledModuleResolution(t *testing.T) {
	env := runtimeEnvironment([]string{"PATH=/native", "NODE_PATH=/foreign", "node_options=--require foreign", "Playwright_Mcp_Config=foreign", "PLAYWRIGHT_BROWSERS_PATH=/foreign"}, "/bundled")
	if !slices.Equal(env, []string{"PATH=/native", "PLAYWRIGHT_BROWSERS_PATH=/bundled"}) {
		t.Fatalf("ambient engine configuration survived: %v", env)
	}
}

func TestProbeDiagnosticsDrainBeyondBound(t *testing.T) {
	var output probeOutput
	data := make([]byte, 128*1024)
	if count, err := output.Write(data); count != len(data) || err != nil {
		t.Fatalf("pipe cannot drain: %d %v", count, err)
	}
	if output.Len() != 64*1024 {
		t.Fatalf("unbounded diagnostics: %d", output.Len())
	}
}
