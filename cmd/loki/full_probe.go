package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	appnetwork "loki/internal/app/network"
	"loki/internal/auth"
	"loki/internal/daemon"
	"loki/internal/rpc"
	"loki/internal/transport/toolproxy"
)

type probeAuthorization struct {
	token string
	base  http.RoundTripper
}

func (p probeAuthorization) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header = request.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+p.token)
	return p.base.RoundTrip(copy)
}

// The backend executes probes with the actual permitted service client UID.
// Browser probes use MCP initialization/discovery, not a connect-only health
// check. They create and close their own protected engine sessions.
func runFullProbe(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("full-probe", flag.ContinueOnError)
	flags.SetOutput(stderr)
	role := flags.String("role", "", "runtime, executor, launcher, browser, signing, proxy or mcp")
	socket := flags.String("socket", "", "protected role socket")
	uid := flags.Int64("expected-uid", -1, "explicit expected Unix peer UID")
	address := flags.String("address", "", "loopback MCP endpoint")
	tokenPath := flags.String("token-file", "", "private MCP token file")
	module := flags.String("module", "", "optional runtime module readiness")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := func() error {
		switch *role {
		case "module":
			if *module != "git" && *module != "workspace" {
				return errors.New("unsupported native module readiness probe")
			}
			var layout mcpLayout
			if err := daemon.ReadJSON("/etc/loki/layout/mcp.json", &layout); err != nil {
				return errors.New("native module probe requires its owned MCP layout")
			}
			options, err := layout.options("")
			if err != nil || options.AuthorizeTool == nil {
				return errors.New("native module probe requires current activation")
			}
			return options.AuthorizeTool(ctx, *module, "")
		case "endpoints":
			if *uid < 0 || *uid > 4294967295 {
				return errors.New("endpoint probe requires expected peer identity")
			}
			connection, err := (appnetwork.EndpointDialer{Socket: *socket, UID: uint32(*uid)}).Dial(ctx, 0)
			if err != nil {
				return err
			}
			return connection.Close()
		case "signing":
			if *uid < 0 || *uid > 4294967295 {
				return errors.New("signing probe requires its expected UID")
			}
			connection, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", *socket)
			if err != nil {
				return err
			}
			defer connection.Close()
			peer, err := rpc.PeerCredentials(connection.(*net.UnixConn))
			if err != nil {
				return err
			}
			if peer.UID != uint32(*uid) {
				return errors.New("signing peer identity differs from its protected role")
			}
			connection.SetDeadline(time.Now().Add(5 * time.Second))
			if _, err := connection.Write([]byte{0, 0, 0, 1, 11}); err != nil {
				return err
			}
			var header [4]byte
			if _, err := io.ReadFull(connection, header[:]); err != nil {
				return err
			}
			size := binary.BigEndian.Uint32(header[:])
			if size < 5 || size > 65536 {
				return errors.New("invalid signing readiness response")
			}
			response := make([]byte, int(size))
			if _, err := io.ReadFull(connection, response); err != nil {
				return err
			}
			if response[0] != 12 || binary.BigEndian.Uint32(response[1:5]) == 0 {
				return errors.New("signing key is not loaded; run integrations setup git")
			}
			return nil
		case "proxy":
			endpoint, err := url.Parse(*address)
			if err != nil || endpoint.Scheme != "http" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
				return errors.New("invalid proxy readiness endpoint")
			}
			if ip := net.ParseIP(endpoint.Hostname()); ip == nil || !ip.IsLoopback() {
				return errors.New("proxy readiness must use loopback")
			}
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.Proxy = nil
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("proxy probe redirects are disabled") }}
			request, err := http.NewRequestWithContext(ctx, "GET", *address, nil)
			if err != nil {
				return err
			}
			response, err := client.Do(request)
			if err != nil {
				return err
			}
			defer response.Body.Close()
			// An origin-form request is invalid for both confined proxies.
			// This proves HTTP processing without dialing any destination.
			if response.StatusCode != http.StatusBadRequest {
				return errors.New("proxy did not reject an invalid target as expected")
			}
			return nil
		case "runtime", "executor", "launcher":
			if *uid < 0 || *uid > 4294967295 || *socket == "" {
				return errors.New("role probe requires its expected UID and socket")
			}
			expected := uint32(*uid)
			operation := "health"
			if *role == "runtime" {
				operation = "status"
			}
			data, err := (rpc.Client{Socket: *socket, ExpectedUID: &expected, Limits: rpc.Limits{Timeout: 5 * time.Second}}).Call(ctx, map[string]any{"operation": operation})
			if err != nil {
				return err
			}
			var result struct {
				Initialized bool            `json:"initialized"`
				Tools       map[string]bool `json:"tools"`
				Github      struct {
					Configured          bool `json:"configured"`
					CredentialAvailable bool `json:"credential_available"`
					Targets             int  `json:"target_count"`
				} `json:"github"`
			}
			if err := json.Unmarshal(data, &result); err != nil {
				return err
			}
			if !result.Initialized {
				return errors.New("role has not initialized")
			}
			if *module != "" {
				if *role != "runtime" || !result.Tools[*module] {
					return errors.New("requested public runtime module is disabled or uninitialized")
				}
				if *module == "github" && (!result.Github.Configured || !result.Github.CredentialAvailable || result.Github.Targets == 0) {
					return errors.New("GitHub needs App configuration, a provider key and installation targets; run integrations setup github")
				}
			}
			return nil
		case "browser":
			if *uid <= 0 || *uid > 4294967295 {
				return errors.New("browser probe requires its separate non-root peer UID")
			}
			return toolproxy.ProbeBrowser(ctx, *socket, uint32(*uid))
		case "mcp":
			endpoint, err := url.Parse(*address)
			if err != nil || endpoint.Scheme != "http" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "/mcp" {
				return errors.New("MCP probe requires an explicit loopback HTTP endpoint")
			}
			if ip := net.ParseIP(endpoint.Hostname()); ip == nil || !ip.IsLoopback() {
				return errors.New("MCP probe endpoint must be loopback")
			}
			token, err := auth.LoadToken(*tokenPath)
			if err != nil {
				return err
			}
			transport := http.DefaultTransport.(*http.Transport).Clone()
			defer transport.CloseIdleConnections()
			transport.Proxy = nil
			client := &http.Client{Transport: probeAuthorization{token: token, base: transport}, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("MCP probe redirects are disabled") }}
			return toolproxy.ProbeHTTP(ctx, *address, client)
		default:
			return errors.New("unknown full service probe role")
		}
	}()
	if err != nil {
		fmt.Fprintln(stderr, "Full service probe failed:", err)
		return 1
	}
	return 0
}
