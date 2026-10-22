package privateio

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

// Bootstrap is the single private stdin frame consumed before application
// exec. Env entries are records so strict decoding can reject object aliases
// without treating uppercase environment names as protocol field names.
type Bootstrap struct {
	Protocol    int                `json:"protocol"`
	Environment []EnvironmentEntry `json:"environment"`
}

type EnvironmentEntry struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

var bootstrapName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// ValidateBootstrap uses the actual Linux stack/page limits supplied by the
// receiver. Pointer overhead is conservatively budgeted for 64-bit platforms.
func ValidateBootstrap(input Bootstrap, argv []string, stackLimit uint64, pageSize int) ([]string, error) {
	invalid := errors.New("private bootstrap rejected input")
	if len(argv) == 0 || len(argv) > 256 || !filepath.IsAbs(argv[0]) || filepath.Clean(argv[0]) != argv[0] || argv[0] == "/" || input.Protocol != 1 || input.Environment == nil || len(input.Environment) > 1024 || pageSize < 4096 {
		return nil, invalid
	}
	limit := max(uint64(128<<10), min(uint64(6<<20), stackLimit/4))
	perString := pageSize * 32
	total := uint64((len(argv) + len(input.Environment) + 2) * 8)
	for _, arg := range argv {
		if strings.ContainsRune(arg, 0) || len(arg)+1 > perString {
			return nil, invalid
		}
		total += uint64(len(arg) + 1)
	}
	seen := map[string]bool{}
	env := make([]string, 0, len(input.Environment))
	for _, entry := range input.Environment {
		if !bootstrapName.MatchString(entry.Name) || seen[entry.Name] || strings.ContainsRune(entry.Value, 0) || len(entry.Name)+len(entry.Value)+2 > perString {
			return nil, invalid
		}
		seen[entry.Name] = true
		value := entry.Name + "=" + entry.Value
		total += uint64(len(value) + 1)
		if total > limit {
			return nil, invalid
		}
		env = append(env, value)
	}
	if total > limit {
		return nil, invalid
	}
	return env, nil
}
