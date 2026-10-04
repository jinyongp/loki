package packaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeImageCacheHasIndependentBoundedImportExport(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "maintainer", "tools", "build_full_images.py"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, value := range []string{"ThreadPoolExecutor(max_workers=2)", "timeout=60", "type=cacheonly", "mode=max,ignore-error=true", "building from verified local inputs", "rewrite-timestamp=true"} {
		if !strings.Contains(text, value) {
			t.Fatal("native image/cache contract missing:", value)
		}
	}
}
