package progress

import (
	"io"
	"os"
	"strings"
)

type verboseWriter struct{ io.Writer }

func (verboseWriter) Verbose() bool { return true }

func WithVerbose(writer io.Writer) io.Writer {
	if writer == nil {
		return nil
	}
	return verboseWriter{writer}
}

// Verbose also recognizes the environment flag forwarded to child processes.
// It never changes the process environment or another command's output mode.
func Verbose(value any) bool {
	if setting, ok := value.(interface{ Verbose() bool }); ok {
		return setting.Verbose()
	}
	return os.Getenv("LOKI_VERBOSE") == "1"
}

// CLIArguments handles the global option before dispatching a subcommand.
func CLIArguments(args []string, stderr io.Writer) ([]string, io.Writer) {
	if len(args) > 0 && args[0] == "--verbose" {
		return args[1:], WithVerbose(stderr)
	}
	return args, stderr
}

// VerboseEnvironment retains all existing child settings and forwards only the
// output preference. WSLENV's /u flag carries it from Windows into Linux.
func VerboseEnvironment(base []string) []string {
	result := make([]string, 0, len(base)+2)
	var entries []string
	for _, setting := range base {
		key, value, _ := strings.Cut(setting, "=")
		switch {
		case strings.EqualFold(key, "LOKI_VERBOSE"):
		case strings.EqualFold(key, "WSLENV"):
			for _, entry := range strings.Split(value, ":") {
				name, _, _ := strings.Cut(entry, "/")
				if entry != "" && !strings.EqualFold(name, "LOKI_VERBOSE") {
					entries = append(entries, entry)
				}
			}
		default:
			result = append(result, setting)
		}
	}
	result = append(result, "LOKI_VERBOSE=1", "WSLENV="+strings.Join(append(entries, "LOKI_VERBOSE/u"), ":"))
	return result
}
