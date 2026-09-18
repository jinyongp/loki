package egress

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"loki/internal/browsernet"
	"loki/internal/platform/netguard"
)

func TestFixedDownloadAllowlist(t *testing.T) {
	policy := testPolicy(Profile{AllowedHosts: []string{"github.com", "registry.npmjs.org"}, AllowedPorts: []int{443}})
	decisions := []Decision{}
	proxy, err := New(policy, "dependency-install", func(decision Decision) { decisions = append(decisions, decision) })
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	forwarded := ""
	proxy.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded = r.Host; w.WriteHeader(200) })
	for _, host := range policy.Profiles["dependency-install"].AllowedHosts {
		for _, authority := range []string{host + ":443", strings.ToUpper(host) + ".:443"} {
			r := httptest.NewRequest("CONNECT", "http://fixture", nil)
			r.RequestURI = authority
			r.Host = "untrusted.example:443"
			w := httptest.NewRecorder()
			proxy.ServeHTTP(w, r)
			if w.Code != 200 || forwarded != host+":443" {
				t.Fatal(authority, w.Code, forwarded)
			}
		}
	}
	for _, test := range []struct {
		method, authority string
		status            int
	}{
		{"CONNECT", "registry.npmjs.org:80", 403}, {"CONNECT", "evil.registry.npmjs.org:443", 403}, {"CONNECT", "registry.npmjs.org.evil.test:443", 403},
		{"CONNECT", "127.0.0.1:443", 403}, {"CONNECT", "github.com", 400}, {"CONNECT", "user@github.com:443", 403}, {"GET", "github.com:443", 403},
		{"CONNECT", strings.Repeat("x", 8192) + ":443", 431},
	} {
		forwarded = ""
		r := httptest.NewRequest(test.method, "http://fixture", nil)
		r.RequestURI = test.authority
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, r)
		if w.Code != test.status || forwarded != "" {
			t.Fatal(test, w.Code, forwarded)
		}
	}
	r := httptest.NewRequest("CONNECT", "http://fixture", nil)
	r.RequestURI = "github.com:443"
	r.Header.Set("X-Large", strings.Repeat("x", 65536))
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, r)
	if w.Code != 431 {
		t.Fatal(w.Code)
	}
	if len(decisions) == 0 || !decisions[0].Allowed || decisions[len(decisions)-1].Reason != "request-too-large" {
		t.Fatalf("audit decisions = %#v", decisions)
	}
}

func TestAllowedHostResolvingToPrivateAddressIsBlocked(t *testing.T) {
	policy := testPolicy(Profile{AllowedHosts: []string{"registry.npmjs.org"}, AllowedPorts: []int{443}})
	proxy, err := New(policy, "dependency-install", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	network := browsernet.New(netguard.Policy{Lookup: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}})
	network.TunnelErrorStatus = http.StatusBadGateway
	defer network.Close()
	proxy.handler = network
	r := httptest.NewRequest("CONNECT", "http://fixture", nil)
	r.RequestURI = "registry.npmjs.org:443"
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, r)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("private DNS target status = %d", w.Code)
	}
}

func TestRedirectTargetRequiresAnotherAllowlistedTunnel(t *testing.T) {
	policy := testPolicy(Profile{AllowedHosts: []string{"cdn.playwright.dev"}, AllowedPorts: []int{443}})
	proxy, err := New(policy, "github-api", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	proxy.handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	for authority, want := range map[string]int{
		"api.github.com:443":         http.StatusOK,
		"redirect.attacker.test:443": http.StatusForbidden,
	} {
		r := httptest.NewRequest("CONNECT", "http://fixture", nil)
		r.RequestURI = authority
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("redirect tunnel %q status = %d, want %d", authority, w.Code, want)
		}
	}
}

func testPolicy(dependency Profile) Policy {
	return Policy{Version: PolicyVersion, Profiles: map[string]Profile{
		"dependency-install": dependency,
		"github-api":         {AllowedHosts: []string{"api.github.com"}, AllowedPorts: []int{443}},
	}}
}
