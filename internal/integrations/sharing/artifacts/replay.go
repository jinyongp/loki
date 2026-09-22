package artifacts

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

var (
	requestIDPattern          = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	requestFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	ErrRequestConflict        = errors.New("artifact request_id was already used for a different publication")
	ErrReplayCapacityFull     = errors.New("temporary artifact replay capacity is full")
)

type publishReplay struct {
	Fingerprint string
	Result      map[string]any
	Expires     time.Time
}

func ValidRequestID(id string) bool {
	return requestIDPattern.MatchString(id)
}

func clonePublication(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func fullPublication(link map[string]any, data []byte, filename, mime, digest, disposition string) map[string]any {
	result := clonePublication(link)
	result["filename"] = filename
	result["mime_type"] = mime
	result["bytes"] = len(data)
	result["sha256"] = digest
	result["disposition"] = disposition
	return result
}

func (s *Store) purgeReplay(now time.Time) {
	order := s.requestOrder[:0]
	for _, requestID := range s.requestOrder {
		replay, ok := s.requests[requestID]
		if !ok {
			continue
		}
		if !replay.Expires.After(now) {
			delete(s.requests, requestID)
			continue
		}
		order = append(order, requestID)
	}
	s.requestOrder = order
}

func validateReplayIdentity(requestID, fingerprint string) (string, error) {
	requestID = strings.ToLower(requestID)
	if !requestIDPattern.MatchString(requestID) {
		return "", errors.New("artifact request_id must be a UUID")
	}
	if !requestFingerprintPattern.MatchString(fingerprint) {
		return "", errors.New("artifact request fingerprint must be a SHA-256 digest")
	}
	return requestID, nil
}

func (s *Store) replayLocked(requestID, fingerprint string) (map[string]any, bool, error) {
	replay, ok := s.requests[requestID]
	if !ok {
		return nil, false, nil
	}
	if replay.Fingerprint != fingerprint {
		return nil, false, ErrRequestConflict
	}
	return clonePublication(replay.Result), true, nil
}

func (s *Store) Replay(requestID, fingerprint string) (map[string]any, bool, error) {
	requestID, err := validateReplayIdentity(requestID, fingerprint)
	if err != nil {
		return nil, false, err
	}
	s.replayMu.Lock()
	defer s.replayMu.Unlock()
	s.purgeReplay(s.options.Clock())
	return s.replayLocked(requestID, fingerprint)
}

func (s *Store) PublishReplay(requestID, fingerprint string, data []byte, filename, mime, digest string, ttl int, disposition string, extras ...map[string]any) (map[string]any, error) {
	requestID, err := validateReplayIdentity(requestID, fingerprint)
	if err != nil {
		return nil, err
	}
	if len(extras) > 1 {
		return nil, errors.New("artifact replay metadata accepts at most one map")
	}
	var extra map[string]any
	if len(extras) == 1 {
		reserved := map[string]bool{
			"share_id": true, "url": true, "expires_at": true, "filename": true,
			"mime_type": true, "bytes": true, "sha256": true, "disposition": true,
		}
		extra = extras[0]
		for key := range extra {
			if reserved[key] {
				return nil, errors.New("artifact replay metadata uses a reserved publication field")
			}
		}
	}
	s.replayMu.Lock()
	defer s.replayMu.Unlock()
	now := s.options.Clock()
	s.purgeReplay(now)
	if replay, ok, replayErr := s.replayLocked(requestID, fingerprint); ok || replayErr != nil {
		return replay, replayErr
	}
	if len(s.requests) >= s.replayMax {
		return nil, ErrReplayCapacityFull
	}
	link, err := s.Publish(data, filename, mime, digest, ttl, disposition)
	if err != nil {
		return nil, err
	}
	result := fullPublication(link, data, filename, mime, digest, disposition)
	for key, value := range extra {
		result[key] = value
	}
	s.requests[requestID] = publishReplay{
		Fingerprint: fingerprint,
		Result:      clonePublication(result),
		Expires:     now.Add(time.Duration(ttl) * time.Second),
	}
	s.requestOrder = append(s.requestOrder, requestID)
	return result, nil
}
