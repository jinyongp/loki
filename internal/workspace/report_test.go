package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReports(t *testing.T) {
	f := fixture(t)
	for _, tc := range []struct {
		name, content string
		tests         int
		valid         bool
	}{
		{"one.xml", `<testsuite tests="2" failures="1"><testcase/></testsuite>`, 2, true},
		{"many.xml", `<testsuites><testsuite tests="2"/><testsuite tests="3"/></testsuites>`, 5, true},
		{"nested.xml", `<testsuite tests="4"><testsuite tests="2"/></testsuite>`, 4, true},
		{"bad.xml", `<testsuite>`, 0, false},
		{"double.xml", `<testsuite/><testsuite/>`, 0, false},
		{"entity.xml", `<!DOCTYPE testsuite [<!ENTITY x SYSTEM "file:///etc/passwd">]><testsuite>&x;</testsuite>`, 0, false},
		{"count.xml", `<testsuite tests="bad"/>`, 0, false},
	} {
		if err := os.WriteFile(filepath.Join(f.Policy.Root(), tc.name), []byte(tc.content), 0600); err != nil {
			t.Fatal(err)
		}
		content, stats, err := f.TestReport(tc.name)
		if (err == nil) != tc.valid {
			t.Fatalf("%s %v", tc.name, err)
		}
		if tc.valid && (content != tc.content || stats["tests"] != tc.tests) {
			t.Fatal(content, stats)
		}
	}
}
