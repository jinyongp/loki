package buildinfo

import "strings"

var (
	Version = "0.48.0-dev"
	Commit  = ""
	Date    = ""
)

func String() string {
	parts := []string{"loki", Version}
	if Commit != "" {
		parts = append(parts, Commit)
	}
	if Date != "" {
		parts = append(parts, Date)
	}
	return strings.Join(parts, " ")
}
