package privateio

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const MaxMigrationFrame = 500000

// MigrationExpectedValue is borrowed private host input. Explicit encoding below
// is allowed only for the authenticated host-to-staged-broker byte frame.
type MigrationExpectedValue struct {
	Name  string
	Value []byte
}

func (MigrationExpectedValue) Format(s fmt.State, _ rune)   { io.WriteString(s, "[REDACTED]") }
func (MigrationExpectedValue) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }
func (MigrationExpectedValue) MarshalText() ([]byte, error) { return nil, ErrInvalid }

type migrationExpectedWire struct {
	Values []struct {
		Name  string `json:"name"`
		Value []byte `json:"value"`
	} `json:"values"`
}

func validMigrationExpected(values []MigrationExpectedValue) bool {
	if len(values) == 0 || len(values) > 256 {
		return false
	}
	for index, value := range values {
		if !bootstrapName.MatchString(value.Name) || len(value.Value) > 65536 || !utf8.Valid(value.Value) || strings.ContainsRune(string(value.Value), 0) || index > 0 && values[index-1].Name >= value.Name {
			return false
		}
	}
	return true
}

func EncodeMigrationExpected(values []MigrationExpectedValue) ([]byte, error) {
	if !validMigrationExpected(values) {
		return nil, ErrInvalid
	}
	wire := migrationExpectedWire{}
	for _, v := range values {
		body := v.Value
		if body == nil {
			body = []byte{}
		}
		wire.Values = append(wire.Values, struct {
			Name  string `json:"name"`
			Value []byte `json:"value"`
		}{v.Name, body})
	}
	body, err := json.Marshal(wire)
	if err != nil || len(body) > MaxMigrationFrame {
		clear(body)
		return nil, ErrLimit
	}
	return body, nil
}

// DecodeMigrationExpected returns owned sensitive byte slices. The receiver
// must ClearMigrationExpected on every path, including callback/error paths.
func DecodeMigrationExpected(body []byte) ([]MigrationExpectedValue, error) {
	if len(body) == 0 || len(body) > MaxMigrationFrame {
		return nil, ErrLimit
	}
	var wire migrationExpectedWire
	if Decode(body, &wire) != nil {
		for _, v := range wire.Values {
			clear(v.Value)
		}
		return nil, ErrInvalid
	}
	values := make([]MigrationExpectedValue, 0, len(wire.Values))
	for _, v := range wire.Values {
		values = append(values, MigrationExpectedValue{Name: v.Name, Value: v.Value})
	}
	if !validMigrationExpected(values) {
		ClearMigrationExpected(values)
		return nil, ErrInvalid
	}
	return values, nil
}

func ClearMigrationExpected(values []MigrationExpectedValue) {
	for index := range values {
		clear(values[index].Value)
		values[index].Value = nil
	}
}
