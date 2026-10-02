package githubsetup

import (
	"fmt"
	"io"
	"time"
)

// UserStatus contains only personal Projects authorization metadata.
type UserStatus struct {
	Status   string            `json:"status"`
	Accounts []UserAccountView `json:"accounts,omitempty"`
}

func RenderUserStatus(output io.Writer, status UserStatus) {
	fmt.Fprintf(output, "  Personal Projects (optional): %s\n", status.Status)
	for _, account := range status.Accounts {
		fmt.Fprintf(output, "    %s: %s", account.Account, account.Status)
		if !account.ExpiresAt.IsZero() {
			fmt.Fprintf(output, " (expires %s)", account.ExpiresAt.UTC().Format(time.RFC3339))
		}
		fmt.Fprintln(output)
	}
}
