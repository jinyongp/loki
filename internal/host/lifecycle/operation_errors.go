package lifecycle

import (
	"strings"
	"unicode/utf8"
)

// operationErrorMessage bounds diagnostics at the journal boundary. A verbose
// subprocess error must never prevent recovery from being recorded or run.
func operationErrorMessage(message string) string {
	message = strings.TrimSpace(strings.ReplaceAll(strings.ToValidUTF8(message, "�"), "\x00", "�"))
	if message == "" {
		return "operation failed without diagnostic text"
	}
	if len(message) <= maxOperationErrorBytes {
		return message
	}
	const marker = "\n... [diagnostic truncated] ...\n"
	headEnd := maxOperationErrorBytes / 4
	for !utf8.RuneStart(message[headEnd]) {
		headEnd--
	}
	tailStart := len(message) - (maxOperationErrorBytes - headEnd - len(marker))
	for !utf8.RuneStart(message[tailStart]) {
		tailStart++
	}
	return message[:headEnd] + marker + message[tailStart:]
}
