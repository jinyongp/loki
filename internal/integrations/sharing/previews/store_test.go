package previews

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPublishReplayIdentityAndConflict(t *testing.T) {
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	s := New("preview.test", 2, func() time.Time { return now })
	requestID := "70000000-0000-4000-8000-000000000001"
	routes := map[string]int{"/": 3000, "/api": 4000}

	first, err := s.PublishReplay(requestID, routes, "/workspace/one", "node one", 900)
	if err != nil {
		t.Fatal(err)
	}
	replayed, ok, err := s.Replay(requestID, routes, 900)
	if err != nil || !ok || replayed["share_id"] != first["share_id"] || replayed["url"] != first["url"] {
		t.Fatalf("replay = %#v ok=%v err=%v", replayed, ok, err)
	}
	derivedChanged, err := s.PublishReplay(requestID, routes, "/workspace/two", "node two", 900)
	if err != nil || derivedChanged["share_id"] != first["share_id"] {
		t.Fatalf("derived metadata changed replay = %#v, %v", derivedChanged, err)
	}
	if _, _, err := s.Replay(requestID, map[string]int{"/": 3001}, 900); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("changed routes error = %v", err)
	}
	if _, err := s.PublishReplay(requestID, routes, "/workspace", "node", 901); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("changed ttl error = %v", err)
	}
	if s.Revoke(first["share_id"].(string)) == nil || len(s.List()) != 0 {
		t.Fatal("revoke did not remove live share")
	}
	afterRevoke, ok, err := s.Replay(requestID, routes, 900)
	if err != nil || !ok || afterRevoke["share_id"] != first["share_id"] || len(s.List()) != 0 {
		t.Fatalf("replay after revoke = %#v ok=%v err=%v list=%#v", afterRevoke, ok, err, s.List())
	}
	now = now.Add(15 * time.Minute)
	if replay, ok, err := s.Replay(requestID, routes, 900); err != nil || ok || replay != nil {
		t.Fatalf("expired replay = %#v ok=%v err=%v", replay, ok, err)
	}
	if _, err := s.PublishReplay("not-a-uuid", routes, "", "", 900); err == nil {
		t.Fatal("invalid request_id accepted")
	}
}

func TestRoutesAndLifetime(t *testing.T) {
	now := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	s := New("Example.Test.", 1, func() time.Time { return now })
	r, err := s.Publish(map[string]int{"/": 3000, "/_loki/api": 4000, "/api": 5000}, "/workspace", "node", 60)
	if err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(r["url"].(string), "https://")
	p, ok := s.ResolveHost(strings.ToUpper(host) + ".:443")
	if !ok {
		t.Fatal(host)
	}
	for _, tc := range []struct {
		path, next string
		port       int
		ok         bool
	}{{"/", "/", 3000, true}, {"/_loki/api", "/", 4000, true}, {"/_loki/api/x", "/x", 4000, true}, {"/_loki/unknown", "/_loki/unknown", 3000, false}, {"/api/x", "/x", 5000, true}, {"/apix", "/apix", 3000, true}} {
		r, next, ok := ResolveRoute(p, tc.path)
		if next != tc.next || r.Port != tc.port || ok != tc.ok {
			t.Fatalf("%+v => %+v %s %v", tc, r, next, ok)
		}
	}
	if _, err = s.Publish(map[string]int{"/": 3001}, "", "", 60); err == nil {
		t.Fatal("capacity")
	}
	if _, ok = s.ResolveHost(host + ".evil"); ok {
		t.Fatal("host suffix")
	}
	p.Routes[0].Port = 9999
	p2, _ := s.ResolveHost(host)
	if p2.Routes[0].Port == 9999 {
		t.Fatal("mutable internal route")
	}
	now = now.Add(time.Minute)
	if len(s.List()) != 0 || s.Get(r["share_id"].(string)) != nil {
		t.Fatal("expiry")
	}
	r, err = s.Publish(map[string]int{"/": 3000}, "", "", 60)
	if err != nil {
		t.Fatal(err)
	}
	if s.Revoke(r["share_id"].(string)) == nil || len(s.List()) != 0 {
		t.Fatal("revoke")
	}
	for _, routes := range []map[string]int{{"/api": 2}, {"/": 0}, {"/": 2, "/a/": 3}, {"/": 2, "/a/../b": 3}, {"/": 2, "//a": 3}} {
		if _, err = Normalize(routes); err == nil {
			t.Fatal(routes)
		}
	}
}
