// Package previews manages temporary capability URLs for development servers.
package previews

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Route struct {
	Prefix  string
	Port    int
	JobID   string
	LeaseID string
}
type Preview struct {
	ID, Token, CWD, Command string
	Routes                  []Route
	Created, Expires        time.Time
}
type publishReplay struct {
	Fingerprint string
	Result      map[string]any
	Expires     time.Time
}
type Store struct {
	mu           sync.Mutex
	domain       string
	maximum      int
	replayMax    int
	clock        func() time.Time
	items        map[string]Preview
	order        []string
	requests     map[string]publishReplay
	requestOrder []string
}

var idPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)
var hostPattern = regexp.MustCompile(`^loki-([0-9a-f]{32})$`)
var requestIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var routeAuthorityPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

var (
	ErrRequestConflict    = errors.New("preview request_id was already used for a different publication")
	ErrCapacityFull       = errors.New("temporary preview capacity is full")
	ErrReplayCapacityFull = errors.New("temporary preview replay capacity is full")
)

func ValidShareID(id string) bool   { return idPattern.MatchString(id) }
func ValidRequestID(id string) bool { return requestIDPattern.MatchString(id) }

func New(domain string, maximum int, clock func() time.Time) *Store {
	if maximum == 0 {
		maximum = 8
	}
	if clock == nil {
		clock = time.Now
	}
	return &Store{
		domain: strings.ToLower(strings.TrimRight(domain, ".")), maximum: maximum,
		replayMax: max(64, maximum*16), clock: clock,
		items: map[string]Preview{}, requests: map[string]publishReplay{},
	}
}

func Normalize(routes map[string]int) ([]Route, error) {
	result := make([]Route, 0, len(routes))
	for prefix, port := range routes {
		result = append(result, Route{Prefix: prefix, Port: port})
	}
	return NormalizeRoutes(result)
}

func NormalizeRoutes(routes []Route) ([]Route, error) {
	if len(routes) < 1 || len(routes) > 8 {
		return nil, errors.New("preview routes must include a root route")
	}
	result := append([]Route(nil), routes...)
	seenPrefixes := make(map[string]bool, len(result))
	hasRoot := false
	for _, route := range result {
		prefix := route.Prefix
		if !strings.HasPrefix(prefix, "/") || prefix != "/" && strings.HasSuffix(prefix, "/") ||
			strings.Contains(prefix, "//") || route.Port < 1 || route.Port > 65535 || seenPrefixes[prefix] {
			return nil, errors.New("preview route is invalid")
		}
		for _, part := range strings.Split(prefix, "/") {
			if part == ".." {
				return nil, errors.New("preview route is invalid")
			}
		}
		hasAuthority := route.JobID != "" || route.LeaseID != ""
		if hasAuthority && (!routeAuthorityPattern.MatchString(route.JobID) || !routeAuthorityPattern.MatchString(route.LeaseID)) {
			return nil, errors.New("preview route authority is invalid")
		}
		seenPrefixes[prefix] = true
		hasRoot = hasRoot || prefix == "/"
	}
	if !hasRoot {
		return nil, errors.New("preview routes must include a root route")
	}
	sort.Slice(result, func(i, j int) bool {
		if len(result[i].Prefix) != len(result[j].Prefix) {
			return len(result[i].Prefix) > len(result[j].Prefix)
		}
		return result[i].Prefix < result[j].Prefix
	})
	return result, nil
}
func ResolveRoute(p Preview, path string) (Route, string, bool) {
	for _, r := range p.Routes {
		if r.Prefix == "/" {
			return r, path, !strings.HasPrefix(path, "/_loki/")
		}
		if path == r.Prefix || strings.HasPrefix(path, r.Prefix+"/") {
			next := strings.TrimPrefix(path, r.Prefix)
			if next == "" {
				next = "/"
			}
			return r, next, true
		}
	}
	return Route{}, "", false
}
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}
func (s *Store) purge() {
	now := s.clock()
	order := s.order[:0]
	for _, id := range s.order {
		p, ok := s.items[id]
		if !ok {
			continue
		}
		if !p.Expires.After(now) {
			delete(s.items, id)
		} else {
			order = append(order, id)
		}
	}
	s.order = order
	requestOrder := s.requestOrder[:0]
	for _, requestID := range s.requestOrder {
		replay, ok := s.requests[requestID]
		if !ok {
			continue
		}
		if !replay.Expires.After(now) {
			delete(s.requests, requestID)
		} else {
			requestOrder = append(requestOrder, requestID)
		}
	}
	s.requestOrder = requestOrder
}
func date(t time.Time) string {
	t = t.UTC().Truncate(time.Microsecond)
	if t.Nanosecond() == 0 {
		return t.Format("2006-01-02T15:04:05+00:00")
	}
	return t.Format("2006-01-02T15:04:05.000000+00:00")
}
func (s *Store) serialize(p Preview) map[string]any {
	u := "https://loki-" + p.Token + "." + s.domain
	routes := map[string]int{}
	for _, r := range p.Routes {
		routes[r.Prefix] = r.Port
	}
	return map[string]any{"share_id": p.ID, "url": u, "port": routes["/"], "routes": routes, "cwd": p.CWD, "command": p.Command, "created_at": date(p.Created), "expires_at": date(p.Expires), "display_markdown": "[Open live preview](" + u + ")"}
}
func clonePreviewResult(value map[string]any) map[string]any {
	copy := make(map[string]any, len(value))
	for key, item := range value {
		if routes, ok := item.(map[string]int); ok {
			cloned := make(map[string]int, len(routes))
			for prefix, port := range routes {
				cloned[prefix] = port
			}
			copy[key] = cloned
			continue
		}
		copy[key] = item
	}
	return copy
}

func publishFingerprint(routes []Route, ttl int) (string, error) {
	encoded, err := json.Marshal(struct {
		Routes []Route
		TTL    int
	}{routes, ttl})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Store) publishRoutes(requestID string, routes []Route, cwd, command string, ttl int) (map[string]any, error) {
	normalized, err := NormalizeRoutes(routes)
	if err != nil {
		return nil, err
	}
	fingerprint := ""
	if requestID != "" {
		fingerprint, err = publishFingerprint(normalized, ttl)
		if err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purge()
	if requestID != "" {
		if replay, ok := s.requests[requestID]; ok {
			if replay.Fingerprint != fingerprint {
				return nil, ErrRequestConflict
			}
			return clonePreviewResult(replay.Result), nil
		}
		if len(s.requests) >= s.replayMax {
			return nil, ErrReplayCapacityFull
		}
	}
	if len(s.items) >= s.maximum {
		return nil, ErrCapacityFull
	}
	id, err := randomToken(8)
	if err != nil {
		return nil, err
	}
	token, err := randomToken(16)
	if err != nil {
		return nil, err
	}
	now := s.clock()
	p := Preview{id, token, cwd, command, normalized, now, now.Add(time.Duration(ttl) * time.Second)}
	s.items[id] = p
	s.order = append(s.order, id)
	result := s.serialize(p)
	if requestID != "" {
		s.requests[requestID] = publishReplay{Fingerprint: fingerprint, Result: clonePreviewResult(result), Expires: p.Expires}
		s.requestOrder = append(s.requestOrder, requestID)
	}
	return clonePreviewResult(result), nil
}

func (s *Store) Publish(routes map[string]int, cwd, command string, ttl int) (map[string]any, error) {
	normalized, err := Normalize(routes)
	if err != nil {
		return nil, err
	}
	return s.publishRoutes("", normalized, cwd, command, ttl)
}

func (s *Store) replayRoutes(requestID string, routes []Route, ttl int) (map[string]any, bool, error) {
	if !requestIDPattern.MatchString(requestID) {
		return nil, false, errors.New("preview request_id must be a UUID")
	}
	normalized, err := NormalizeRoutes(routes)
	if err != nil {
		return nil, false, err
	}
	fingerprint, err := publishFingerprint(normalized, ttl)
	if err != nil {
		return nil, false, err
	}
	requestID = strings.ToLower(requestID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purge()
	replay, ok := s.requests[requestID]
	if !ok {
		return nil, false, nil
	}
	if replay.Fingerprint != fingerprint {
		return nil, false, ErrRequestConflict
	}
	return clonePreviewResult(replay.Result), true, nil
}

func (s *Store) Replay(requestID string, routes map[string]int, ttl int) (map[string]any, bool, error) {
	normalized, err := Normalize(routes)
	if err != nil {
		return nil, false, err
	}
	return s.replayRoutes(requestID, normalized, ttl)
}

func (s *Store) ReplayRoutes(requestID string, routes []Route, ttl int) (map[string]any, bool, error) {
	return s.replayRoutes(requestID, routes, ttl)
}

func (s *Store) PublishReplay(requestID string, routes map[string]int, cwd, command string, ttl int) (map[string]any, error) {
	normalized, err := Normalize(routes)
	if err != nil {
		return nil, err
	}
	return s.PublishRoutesReplay(requestID, normalized, cwd, command, ttl)
}

func (s *Store) PublishRoutesReplay(requestID string, routes []Route, cwd, command string, ttl int) (map[string]any, error) {
	if !requestIDPattern.MatchString(requestID) {
		return nil, errors.New("preview request_id must be a UUID")
	}
	return s.publishRoutes(strings.ToLower(requestID), routes, cwd, command, ttl)
}
func (s *Store) List() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purge()
	out := []map[string]any{}
	for _, id := range s.order {
		out = append(out, s.serialize(s.items[id]))
	}
	return out
}
func (s *Store) Get(id string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purge()
	p, ok := s.items[id]
	if !ok || !idPattern.MatchString(id) {
		return nil
	}
	return s.serialize(p)
}
func (s *Store) Revoke(id string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purge()
	p, ok := s.items[id]
	if !ok || !idPattern.MatchString(id) {
		return nil
	}
	delete(s.items, id)
	s.purge()
	return s.serialize(p)
}
func (s *Store) ResolveHost(host string) (Preview, bool) {
	host = strings.ToLower(strings.TrimRight(strings.SplitN(host, ":", 2)[0], "."))
	suffix := "." + s.domain
	if !strings.HasSuffix(host, suffix) {
		return Preview{}, false
	}
	match := hostPattern.FindStringSubmatch(strings.TrimSuffix(host, suffix))
	if match == nil {
		return Preview{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purge()
	for _, p := range s.items {
		if p.Token == match[1] {
			p.Routes = append([]Route(nil), p.Routes...)
			return p, true
		}
	}
	return Preview{}, false
}
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = map[string]Preview{}
	s.order = nil
	s.requests = map[string]publishReplay{}
	s.requestOrder = nil
}
