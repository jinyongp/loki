package secret

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"

	"loki/internal/fault"
	"loki/internal/state"
)

func mutationFingerprint(operation string, expected uint64, parts ...string) string {
	hash := sha256.New()
	hash.Write([]byte("loki-secret-mutation-v1"))
	hash.Write([]byte{0})
	hash.Write([]byte(operation))
	hash.Write([]byte{0})
	hash.Write([]byte(strconv.FormatUint(expected, 10)))
	for _, part := range parts {
		hash.Write([]byte{0})
		hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func normalizeMutationRequestID(requestID string) (string, error) {
	requestID = strings.ToLower(strings.TrimSpace(requestID))
	if !requestIDPattern.MatchString(requestID) {
		return "", fault.New(fault.CodeInvalidInput, "secret mutation request_id must be a UUID", false, "generate a new UUID request_id")
	}
	return requestID, nil
}

func secretMutationConflict(message string) error {
	return fault.New(fault.CodeConflict, message, false, "inspect secret metadata and retry with the current revision and a new request_id if the mutation is still desired")
}

func replayMutation(document document, requestID, fingerprint string) (map[string]any, bool, error) {
	requests := object(document["requests"])
	if requests == nil {
		return nil, false, nil
	}
	rawRecord, exists := requests[requestID]
	if !exists {
		return nil, false, nil
	}
	record := object(rawRecord)
	if record == nil || record["fingerprint"] != fingerprint {
		return nil, false, secretMutationConflict("secret mutation request_id was already used for different inputs")
	}
	resultJSON, ok := record["result_json"].(string)
	if !ok {
		return nil, false, fault.Error("secret replay record is invalid")
	}
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader([]byte(resultJSON)))
	decoder.UseNumber()
	if decoder.Decode(&result) != nil || result == nil {
		return nil, false, fault.Error("secret replay record is invalid")
	}
	if revision, ok := result["revision"].(json.Number); ok {
		value, parseErr := strconv.ParseUint(revision.String(), 10, 64)
		if parseErr != nil {
			return nil, false, fault.Error("secret replay record is invalid")
		}
		result["revision"] = value
	}
	for _, key := range []string{"bytes", "count"} {
		if number, ok := result[key].(json.Number); ok {
			value, parseErr := strconv.Atoi(number.String())
			if parseErr != nil {
				return nil, false, fault.Error("secret replay record is invalid")
			}
			result[key] = value
		}
	}
	return result, true, nil
}

func requestRevision(record map[string]any) uint64 {
	switch value := record["revision"].(type) {
	case json.Number:
		revision, _ := strconv.ParseUint(value.String(), 10, 64)
		return revision
	case uint64:
		return value
	case int:
		if value > 0 {
			return uint64(value)
		}
	}
	return 0
}

func ensureReplayCapacity(requests map[string]any) {
	if len(requests) < MaxReplayRequests {
		return
	}
	oldestID := ""
	oldestRevision := uint64(math.MaxUint64)
	for requestID, rawRecord := range requests {
		revision := requestRevision(object(rawRecord))
		if oldestID == "" || revision < oldestRevision || revision == oldestRevision && requestID < oldestID {
			oldestID, oldestRevision = requestID, revision
		}
	}
	if oldestID != "" {
		delete(requests, oldestID)
	}
}

func (c Controller) inspectMutationRequest(
	ctx context.Context,
	requestID string,
	fingerprint string,
	expectedRevision uint64,
) (string, map[string]any, bool, error) {
	requestID, err := normalizeMutationRequestID(requestID)
	if err != nil {
		return "", nil, false, err
	}
	if !fingerprintPattern.MatchString(fingerprint) {
		return "", nil, false, errors.New("secret mutation fingerprint is invalid")
	}
	if expectedRevision == math.MaxUint64 {
		return "", nil, false, fault.Error("secret vault revision is exhausted")
	}
	current, revision, err := c.loadSnapshot(ctx)
	if err != nil {
		return "", nil, false, err
	}
	if replay, ok, replayErr := replayMutation(current, requestID, fingerprint); ok || replayErr != nil {
		return requestID, replay, ok, replayErr
	}
	if revision != expectedRevision {
		return "", nil, false, secretMutationConflict("secret vault revision changed")
	}
	return requestID, nil, false, nil
}

func (c Controller) mutateRequest(
	ctx context.Context,
	requestID string,
	fingerprint string,
	expectedRevision uint64,
	change func(document) (map[string]any, error),
) (map[string]any, error) {
	requestID, replay, ok, err := c.inspectMutationRequest(ctx, requestID, fingerprint, expectedRevision)
	if err != nil || ok {
		return replay, err
	}

	var result map[string]any
	nextRevision := expectedRevision + 1
	_, err = c.backend().Update(ctx, &expectedRevision, func(data json.RawMessage) (json.RawMessage, error) {
		document, decodeErr := decode(data)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if replay, ok, replayErr := replayMutation(document, requestID, fingerprint); ok || replayErr != nil {
			if replayErr != nil {
				return nil, replayErr
			}
			result = replay
			return data, nil
		}
		result, decodeErr = change(document)
		if decodeErr != nil {
			return nil, decodeErr
		}
		result["revision"] = nextRevision
		result["request_id"] = requestID
		resultJSON, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return nil, marshalErr
		}
		if len(resultJSON) > MaxReplayResultBytes {
			return nil, errors.New("secret mutation replay result exceeds size limit")
		}
		requests := object(document["requests"])
		if requests == nil {
			requests = map[string]any{}
			document["requests"] = requests
		}
		ensureReplayCapacity(requests)
		requests[requestID] = map[string]any{
			"fingerprint": fingerprint,
			"revision":    nextRevision,
			"result_json": string(resultJSON),
		}
		return json.Marshal(document)
	})
	if err == nil {
		return result, nil
	}
	if errors.Is(err, state.ErrConflict) {
		current, _, loadErr := c.loadSnapshot(ctx)
		if loadErr != nil {
			return nil, loadErr
		}
		if replay, ok, replayErr := replayMutation(current, requestID, fingerprint); ok || replayErr != nil {
			return replay, replayErr
		}
	}
	return nil, publicStateError(err)
}
