// Package previews manages temporary capability URLs for development servers.
package previews

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Route struct {
	Prefix string
	Port   int
}
type Preview struct {
	ID, Token, CWD, Command string
	Routes                  []Route
	Created, Expires        time.Time
}
type Store struct {
	mu      sync.Mutex
	domain  string
	maximum int
	clock   func() time.Time
	items   map[string]Preview
	order   []string
}

var idPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)
var hostPattern = regexp.MustCompile(`^loki-([0-9a-f]{32})$`)

func ValidShareID(id string) bool { return idPattern.MatchString(id) }

func New(domain string, maximum int, clock func() time.Time) *Store {
	if maximum == 0 {
		maximum = 8
	}
	if clock == nil {
		clock = time.Now
	}
	return &Store{domain: strings.ToLower(strings.TrimRight(domain, ".")), maximum: maximum, clock: clock, items: map[string]Preview{}}
}

func Normalize(routes map[string]int) ([]Route, error) {
	if _, ok := routes["/"]; !ok || len(routes) < 1 || len(routes) > 8 {
		return nil, errors.New("preview routes must include a root route")
	}
	result := make([]Route, 0, len(routes))
	for prefix, port := range routes {
		if !strings.HasPrefix(prefix, "/") || prefix != "/" && strings.HasSuffix(prefix, "/") || strings.Contains(prefix, "//") || port < 1 || port > 65535 {
			return nil, errors.New("preview route is invalid")
		}
		for _, part := range strings.Split(prefix, "/") {
			if part == ".." {
				return nil, errors.New("preview route is invalid")
			}
		}
		result = append(result, Route{prefix, port})
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
func (s *Store) Publish(routes map[string]int, cwd, command string, ttl int) (map[string]any, error) {
	normalized, err := Normalize(routes)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purge()
	if len(s.items) >= s.maximum {
		return nil, errors.New("temporary preview capacity is full; stop or wait for a preview to expire")
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
	return s.serialize(p), nil
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
}
