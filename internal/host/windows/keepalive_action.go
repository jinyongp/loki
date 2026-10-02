package windows

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf16"
)

func hiddenKeepaliveArguments(executable, arguments string) string {
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	script := fmt.Sprintf(`$ErrorActionPreference='Stop';$p=New-Object System.Diagnostics.Process;$p.StartInfo.FileName=%s;$p.StartInfo.Arguments=%s;$p.StartInfo.UseShellExecute=$false;$p.StartInfo.CreateNoWindow=$true;$null=$p.Start();$p.WaitForExit();exit $p.ExitCode`, quote(executable), quote(arguments))
	units := utf16.Encode([]rune(script))
	raw := make([]byte, len(units)*2)
	for i, unit := range units {
		raw[2*i], raw[2*i+1] = byte(unit), byte(unit>>8)
	}
	return "-NoProfile -NonInteractive -WindowStyle Hidden -EncodedCommand " + base64.StdEncoding.EncodeToString(raw)
}
