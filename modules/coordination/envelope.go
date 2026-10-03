package devtools

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// EnvelopeVersion describes the JSON transport, not the CLI command contract.
const EnvelopeVersion = 1

// decodeEnvelope checks the transport before a response is treated as either
// command data or a trusted CLI failure. Raw response content is never in errors.
func decodeEnvelope(raw []byte) (Envelope, error) {
	var fields struct {
		SchemaVersion *int            `json:"schema_version"`
		OK            *bool           `json:"ok"`
		Data          json.RawMessage `json:"data"`
		Error         json.RawMessage `json:"error"`
	}
	if err := decodeObject(raw, &fields); err != nil {
		return Envelope{}, errors.New("devtools returned an invalid response envelope")
	}
	if fields.SchemaVersion == nil || *fields.SchemaVersion != EnvelopeVersion || fields.OK == nil {
		return Envelope{}, errors.New("devtools returned an unsupported response envelope")
	}
	if *fields.OK {
		if len(fields.Data) == 0 || len(fields.Error) != 0 {
			return Envelope{}, errors.New("devtools returned an inconsistent success envelope")
		}
	} else {
		if len(fields.Data) != 0 || len(fields.Error) == 0 {
			return Envelope{}, errors.New("devtools returned an inconsistent failure envelope")
		}
		var failure struct {
			Code    *string         `json:"code"`
			Message *string         `json:"message"`
			Details json.RawMessage `json:"details"`
		}
		if err := decodeObject(fields.Error, &failure); err != nil || failure.Code == nil || *failure.Code == "" || failure.Message == nil {
			return Envelope{}, errors.New("devtools returned an invalid failure envelope")
		}
		if len(failure.Details) > 0 && !jsonObject(failure.Details) {
			return Envelope{}, errors.New("devtools returned invalid error details")
		}
	}
	return Envelope{SchemaVersion: *fields.SchemaVersion, OK: *fields.OK, Data: fields.Data, Error: fields.Error}, nil
}

func successData(raw []byte) (json.RawMessage, error) {
	envelope, err := decodeEnvelope(raw)
	if err != nil {
		return nil, err
	}
	if !envelope.OK {
		return nil, errors.New("devtools returned a failure envelope")
	}
	return envelope.Data, nil
}

func jsonObject(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed)
}

// decodeObject rejects duplicate transport fields as well as unknown fields,
// null objects and concatenated responses. Nested data uses its command schema.
func decodeObject(raw []byte, target any) error {
	if !jsonObject(raw) {
		return errors.New("expected a JSON object")
	}
	keys := json.NewDecoder(bytes.NewReader(raw))
	if _, err := keys.Token(); err != nil {
		return err
	}
	seen := make(map[string]bool)
	for keys.More() {
		token, err := keys.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		// All fields in these transport objects are lowercase. encoding/json
		// otherwise accepts case-insensitive aliases for struct field tags.
		if !ok || seen[key] || key != strings.ToLower(key) {
			return errors.New("duplicate or invalid JSON field")
		}
		seen[key] = true
		var value json.RawMessage
		if err = keys.Decode(&value); err != nil {
			return err
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("expected one JSON object")
	}
	return nil
}
