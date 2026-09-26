package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeRequestVerifier struct{}

func (fakeRequestVerifier) VerifyRequest(r *http.Request) bool {
	values := r.Header.Values("X-Test-Assertion")
	return len(values) == 1 && values[0] == "signed-token"
}

func TestBearerAndExternalRequestGate(t *testing.T) {
	gate := Gate{Token: strings.Repeat("t", 48), External: fakeRequestVerifier{}}
	for _, tc := range []struct {
		bearer, assertion string
		status            int
	}{
		{"Bearer " + gate.Token, "", 204}, {"", "signed-token", 204}, {"", "", 401}, {"Bearer bad", "bad", 401}, {"bearer " + gate.Token, "", 401},
	} {
		r := httptest.NewRequest("POST", "http://localhost/mcp", nil)
		r.Header.Set("Authorization", tc.bearer)
		r.Header.Set("X-Test-Assertion", tc.assertion)
		w := httptest.NewRecorder()
		gate.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("status=%d want %d", w.Code, tc.status)
		}
		if w.Code == 401 && w.Body.String() != `{"error":"unauthorized"}` {
			t.Fatal(w.Body.String())
		}
	}
	r := httptest.NewRequest("POST", "http://localhost/mcp", nil)
	r.Header.Add("Authorization", "Bearer "+gate.Token)
	r.Header.Add("Authorization", "Bearer wrong")
	if gate.Authorized(r) {
		t.Fatal("accepted duplicate credentials")
	}
}

func TestHostPolicy(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	h := HostPolicy(8765, []string{"mcp.example.com"}, next)
	for _, tc := range []struct {
		host, origin string
		status       int
	}{
		{"127.0.0.1:8765", "", 204},
		{"127.0.0.1:19000", "", 204},
		{"localhost:19000", "", 204},
		{"[::1]:19000", "", 204},
		{"mcp.example.com", "", 204},
		{"mcp.example.com:443", "", 204},
		{"evil.example", "", 421},
		{"192.0.2.1:19000", "", 421},
		{"mcp.example.com", "https://evil.example", 403},
	} {
		r := httptest.NewRequest("POST", "http://"+tc.host+"/mcp", nil)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Errorf("%s %s: %d", tc.host, tc.origin, w.Code)
		}
	}
}
