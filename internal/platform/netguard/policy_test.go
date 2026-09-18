package netguard

import (
	"context"
	"net/netip"
	"testing"
)

func TestDestinationPolicy(t *testing.T) {
	for _, value := range []string{"64:ff9b::a9fe:a9fe", "fec0::1", "2002:7f00:1::1"} {
		if Public(netip.MustParseAddr(value)) {
			t.Fatal(value)
		}
	}
	for _, value := range []string{"http://example.test", "https://example.test/path?q=1", "http://127.0.0.1:43000", "http://127.0.0.1:8765"} {
		if _, err := ValidateURL(value); err != nil {
			t.Fatal(value, err)
		}
	}
	for _, value := range []string{"file:///etc/passwd", "http://user:pass@example.test", "http://localhost:43000", "http://10.0.0.1", "http://[::1]", "http://example.test:8000", "http://169.254.169.254", "http://[::ffff:127.0.0.1]"} {
		if _, err := ValidateURL(value); err == nil {
			t.Fatal(value)
		}
	}
	p := Policy{ValidatePort: func(_ context.Context, port int) bool { return port == 43000 }, Lookup: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.1")}, nil
	}}
	if _, err := p.Addresses(t.Context(), "mixed.example", 443); err == nil {
		t.Fatal("mixed public/private DNS accepted")
	}
	if _, err := p.Addresses(t.Context(), "127.0.0.1", 43000); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Addresses(t.Context(), "127.0.0.1", 18765); err == nil {
		t.Fatal("protected callback port accepted")
	}
	p.ValidatePort = func(context.Context, int) bool { return false }
	if _, err := p.Addresses(t.Context(), "127.0.0.1", 43000); err == nil {
		t.Fatal("unmanaged loopback accepted")
	}
}
