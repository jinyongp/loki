// Package netguard provides shared outbound destination validation and bounded dialing.
package netguard

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var blocked = func() []netip.Prefix {
	result := []netip.Prefix{}
	for _, value := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10"} {
		result = append(result, netip.MustParsePrefix(value))
	}
	return result
}()

func validManagedPort(port int) bool { return port >= 1024 && port <= 65535 }

func Public(address netip.Addr) bool {
	address = address.Unmap()
	// Exclude translation, site-local and reserved IPv6 ranges as well as
	// private destinations embedded in transition addresses.
	if address.Is6() && !netip.MustParsePrefix("2000::/3").Contains(address) {
		return false
	}
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.Zone() != "" {
		return false
	}
	for _, prefix := range blocked {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}
func ValidateURL(value string) (string, error) {
	if value == "" || utf8.RuneCountInString(value) > 4096 || strings.ContainsRune(value, 0) {
		return "", errors.New("url must be a non-empty HTTP or HTTPS URL")
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return "", errors.New("only credential-free HTTP and HTTPS URLs are allowed")
	}
	host := strings.ToLower(strings.TrimRight(u.Hostname(), "."))
	port := 80
	if u.Scheme == "https" {
		port = 443
	}
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return "", errors.New("invalid destination port")
		}
	}
	if host == "127.0.0.1" {
		if !validManagedPort(port) {
			return "", errors.New("local development port is not allowed")
		}
		return value, nil
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return "", errors.New("local and private destinations are blocked")
	}
	if address, err := netip.ParseAddr(host); err == nil && !Public(address) {
		return "", errors.New("local and private destinations are blocked")
	}
	if port != 80 && port != 443 {
		return "", errors.New("only ports 80 and 443 are allowed")
	}
	return value, nil
}

type Policy struct {
	ValidatePort func(context.Context, int) bool
	Lookup       func(context.Context, string) ([]netip.Addr, error)
}

func (p Policy) Addresses(ctx context.Context, host string, port int) ([]netip.Addr, error) {
	host = strings.ToLower(strings.TrimRight(host, "."))
	if host == "127.0.0.1" {
		if !validManagedPort(port) || p.ValidatePort == nil || !p.ValidatePort(ctx, port) {
			return nil, errors.New("workspace development port is not allowed")
		}
		return []netip.Addr{netip.MustParseAddr(host)}, nil
	}
	if port != 80 && port != 443 {
		return nil, errors.New("destination port is blocked")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, errors.New("private destination")
	}
	lookup := p.Lookup
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	addresses, err := lookup(ctx, host)
	if err != nil {
		return nil, errors.New("host resolution failed")
	}
	if len(addresses) == 0 || len(addresses) > 64 {
		return nil, errors.New("host has no public address")
	}
	for _, address := range addresses {
		if !Public(address) {
			return nil, errors.New("private destination")
		}
	}
	return addresses, nil
}
func (p Policy) Dial(ctx context.Context, network, target string) (net.Conn, error) {
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return nil, errors.New("invalid destination")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, errors.New("invalid destination")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	addresses, err := p.Addresses(ctx, host, port)
	if err != nil {
		return nil, err
	}
	for _, address := range addresses {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(address.String(), portText))
		if err == nil {
			return conn, nil
		}
	}
	return nil, errors.New("upstream connection failed")
}
