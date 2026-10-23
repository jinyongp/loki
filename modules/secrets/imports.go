package secret

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"loki/internal/fault"
)

const MaxInboxBytes = 2_000_000

var dotenvNewline = regexp.MustCompile(`\r\n|[\n\r\v\f\x1c-\x1e\x{0085}\x{2028}\x{2029}]`)

func trim(value string) string {
	return strings.TrimFunc(value, func(r rune) bool { return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f })
}

func ParseDotenv(source string) (map[string]string, error) {
	values := map[string]string{}
	for i, raw := range dotenvNewline.Split(source, -1) {
		line := trim(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = trim(line[7:])
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fault.Error(fmt.Sprintf("invalid dotenv assignment at line %d", i+1))
		}
		name, value = trim(name), trim(value)
		if err := SecretName(name); err != nil {
			return nil, err
		}
		if len(value) >= 2 && value[0] == value[len(value)-1] && (value[0] == '\'' || value[0] == '"') {
			if value[0] == '"' {
				var decoded string
				if json.Unmarshal([]byte(value), &decoded) != nil {
					return nil, fault.Error(fmt.Sprintf("invalid quoted dotenv value at line %d", i+1))
				}
				value = decoded
			} else {
				value = value[1 : len(value)-1]
			}
		}
		values[name] = value
	}
	if len(values) == 0 {
		return nil, fault.Error("dotenv import must contain secrets")
	}
	return values, nil
}
