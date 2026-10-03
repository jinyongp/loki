package githubsetup

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestRenderUserStatusShowsEachAccountAndExpiration(t *testing.T) {
	var output bytes.Buffer
	RenderUserStatus(&output, UserStatus{Status: "unconfigured", Accounts: []UserAccountView{
		{Account: "first-user", Status: "expired", ExpiresAt: time.Date(2026, 10, 3, 2, 0, 0, 0, time.FixedZone("KST", 9*60*60))},
		{Account: "second-user", Status: "unconfigured"},
	}})
	for _, want := range []string{"Personal Projects (optional): unconfigured", "first-user: expired (expires 2026-10-02T17:00:00Z)", "second-user: unconfigured"} {
		if !strings.Contains(output.String(), want) {
			t.Fatal("authorization status omitted metadata", output.String(), want)
		}
	}
}
