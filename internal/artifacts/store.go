// Package artifacts serves bounded, revocable, temporary downloads.
package artifacts

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func ValidShareID(id string) bool { return tokenPattern.MatchString(id) }

type Options struct {
	BaseURL            string
	AllowedHosts       []string
	MaxItems, MaxBytes int
	Clock              func() time.Time
}

type item struct {
	data                                []byte
	filename, mime, digest, disposition string
	expires                             time.Time
}

type Store struct {
	mu      sync.Mutex
	options Options
	hosts   map[string]bool
	items   map[string]item
	order   []string
	bytes   int
}

func New(o Options) *Store {
	if o.MaxItems == 0 {
		o.MaxItems = 16
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = 64 * 1024 * 1024
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	o.BaseURL = strings.TrimRight(o.BaseURL, "/")
	s := &Store{options: o, hosts: map[string]bool{}, items: map[string]item{}}
	for _, h := range o.AllowedHosts {
		s.hosts[strings.ToLower(h)] = true
	}
	return s
}

func (s *Store) purge(now time.Time) {
	order := s.order[:0]
	for _, token := range s.order {
		v, ok := s.items[token]
		if !ok {
			continue
		}
		if !v.expires.After(now) {
			delete(s.items, token)
			s.bytes -= len(v.data)
		} else {
			order = append(order, token)
		}
	}
	s.order = order
}

// timestamp uses ISO 8601 with microsecond precision.
func timestamp(t time.Time) string {
	t = t.UTC().Truncate(time.Microsecond)
	if t.Nanosecond() == 0 {
		return t.Format("2006-01-02T15:04:05+00:00")
	}
	return t.Format("2006-01-02T15:04:05.000000+00:00")
}

func (s *Store) link(token string, v item) map[string]any {
	return map[string]any{"share_id": token, "url": s.options.BaseURL + "/" + token, "expires_at": timestamp(v.expires)}
}

func (s *Store) Publish(data []byte, filename, mime, digest string, ttl int, disposition string) (map[string]any, error) {
	if disposition != "inline" && disposition != "attachment" {
		return nil, errors.New("artifact disposition must be inline or attachment")
	}
	for _, c := range mime {
		if c < 32 || c > 126 {
			return nil, errors.New("invalid artifact MIME type")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.options.Clock()
	s.purge(now)
	if len(s.items) >= s.options.MaxItems || len(data) > s.options.MaxBytes-s.bytes {
		return nil, errors.New("temporary artifact capacity is full; wait for links to expire and retry")
	}
	var token string
	for {
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		token = base64.RawURLEncoding.EncodeToString(b[:])
		if _, exists := s.items[token]; !exists {
			break
		}
	}
	v := item{append([]byte(nil), data...), filename, mime, digest, disposition, now.Add(time.Duration(ttl) * time.Second)}
	s.items[token] = v
	s.order = append(s.order, token)
	s.bytes += len(data)
	return s.link(token, v), nil
}

func (s *Store) List() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purge(s.options.Clock())
	result := []map[string]any{}
	for _, token := range s.order {
		v := s.items[token]
		row := s.link(token, v)
		row["filename"], row["mime_type"], row["bytes"], row["sha256"], row["disposition"] = v.filename, v.mime, len(v.data), v.digest, v.disposition
		result = append(result, row)
	}
	return result
}

func (s *Store) Revoke(token string) map[string]any {
	if !tokenPattern.MatchString(token) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purge(s.options.Clock())
	v, ok := s.items[token]
	if !ok {
		return nil
	}
	delete(s.items, token)
	s.bytes -= len(v.data)
	s.purge(s.options.Clock())
	return map[string]any{"share_id": token, "filename": v.filename, "bytes": len(v.data), "sha256": v.digest}
}

func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = map[string]item{}
	s.order = nil
	s.bytes = 0
}

func respond(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(message)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(message))
}

func (s *Store) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.hosts[strings.ToLower(r.Host)] {
		respond(w, 421, "misdirected request")
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		w.Header().Set("Allow", "GET, HEAD")
		respond(w, 405, "method not allowed")
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/artifacts/")
	if r.URL.Path != "/artifacts/"+token || !tokenPattern.MatchString(token) {
		respond(w, 404, "not found")
		return
	}
	s.mu.Lock()
	s.purge(s.options.Clock())
	v, ok := s.items[token]
	s.mu.Unlock()
	if !ok {
		respond(w, 404, "not found")
		return
	}
	h := w.Header()
	h.Set("Content-Type", v.mime)
	h.Set("Content-Length", strconv.Itoa(len(v.data)))
	h.Set("Content-Disposition", v.disposition+`; filename="artifact"; filename*=UTF-8''`+strings.ReplaceAll(url.QueryEscape(v.filename), "+", "%20"))
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	h.Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(200)
	if r.Method != "HEAD" {
		_, _ = w.Write(v.data)
	}
}
