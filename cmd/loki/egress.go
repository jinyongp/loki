package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"loki/internal/audit"
	"loki/internal/daemon"
	"loki/internal/egress"
	"loki/internal/service"
)

const maxEgressForwards = 8

type repeatedFlag []string

func (f *repeatedFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *repeatedFlag) Set(value string) error {
	if len(*f) >= maxEgressForwards {
		return fmt.Errorf("too many TCP forwards")
	}
	*f = append(*f, value)
	return nil
}

type egressForward struct {
	port   int
	target string
}

func validEnvironmentName(value string) bool {
	if value == "" {
		return false
	}
	for index, r := range value {
		if index == 0 {
			if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
				return false
			}
			continue
		}
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func parseEgressForwards(values []string, proxyPort int) ([]egressForward, error) {
	if len(values) > maxEgressForwards {
		return nil, fmt.Errorf("too many TCP forwards")
	}
	result := make([]egressForward, 0, len(values))
	seen := map[int]bool{}
	for _, value := range values {
		listenText, target, ok := strings.Cut(value, "=")
		if !ok || listenText == "" || target == "" {
			return nil, fmt.Errorf("TCP forward must be LISTEN_PORT=TARGET_HOST:PORT")
		}
		listenPort, err := strconv.Atoi(listenText)
		if err != nil || listenPort < 1024 || listenPort > 65535 || listenPort == proxyPort || seen[listenPort] {
			return nil, fmt.Errorf("TCP forward listen port is invalid")
		}
		targetHost, targetPortText, err := net.SplitHostPort(target)
		if err != nil || targetHost == "" || strings.ContainsAny(targetHost, "\x00\r\n") {
			return nil, fmt.Errorf("TCP forward target is invalid")
		}
		targetPort, err := strconv.Atoi(targetPortText)
		if err != nil || targetPort < 1 || targetPort > 65535 {
			return nil, fmt.Errorf("TCP forward target is invalid")
		}
		seen[listenPort] = true
		result = append(result, egressForward{
			port: listenPort, target: net.JoinHostPort(targetHost, strconv.Itoa(targetPort)),
		})
	}
	return result, nil
}

func runEgressProxy(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("egress-proxy", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "127.0.0.1", "proxy listen address")
	port := flags.Int("port", defaultEgressProxyPort, "loopback proxy port")
	contractPath := flags.String("execution-contract", "/usr/share/doc/loki/execution-contract.json", "administrator-owned execution contract")
	policyPath := flags.String("policy", "", "administrator-owned egress policy")
	profile := flags.String("profile", "", "egress policy profile")
	auditPath := flags.String("audit", "", "private egress audit log")
	authTokenEnv := flags.String("auth-token-env", "", "optional environment variable containing the required proxy credential")
	forwardHost := flags.String("forward-host", "", "optional TCP forward listen address")
	forwardPort := flags.Int("forward-port", 0, "legacy single TCP forward listen port")
	forwardTarget := flags.String("forward-target", "", "legacy single TCP forward target")
	var forwardValues repeatedFlag
	flags.Var(&forwardValues, "forward", "repeatable LISTEN_PORT=TARGET_HOST:PORT TCP forward")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	listenIP := net.ParseIP(*host)
	if flags.NArg() != 0 || listenIP == nil || !listenIP.IsUnspecified() && !listenIP.IsLoopback() ||
		*port < 1024 || *port > 65535 || *profile == "" ||
		!filepath.IsAbs(*contractPath) || !filepath.IsAbs(*policyPath) || !filepath.IsAbs(*auditPath) {
		fmt.Fprintln(stderr, "egress-proxy requires port, execution contract, policy, profile, and audit path")
		return 2
	}
	_, contractPort, err := loadExecutionProxy(*contractPath, *profile)
	if err != nil || contractPort != *port {
		fmt.Fprintln(stderr, "egress-proxy port does not match execution contract")
		return 2
	}

	authToken := ""
	if *authTokenEnv != "" {
		if !validEnvironmentName(*authTokenEnv) {
			fmt.Fprintln(stderr, "egress proxy authentication configuration is invalid")
			return 2
		}
		var ok bool
		authToken, ok = os.LookupEnv(*authTokenEnv)
		if !ok || authToken == "" {
			fmt.Fprintln(stderr, "egress proxy authentication is unavailable")
			return 1
		}
	}

	legacyForward := *forwardHost != "" || *forwardPort != 0 || *forwardTarget != ""
	if legacyForward {
		if *forwardHost == "" || *forwardPort == 0 || *forwardTarget == "" {
			fmt.Fprintln(stderr, "TCP forward requires a listen address, port, and target")
			return 2
		}
		forwardValues = append(forwardValues, strconv.Itoa(*forwardPort)+"="+*forwardTarget)
	}
	if len(forwardValues) > 0 {
		forwardIP := net.ParseIP(*forwardHost)
		if forwardIP == nil || !forwardIP.IsUnspecified() && !forwardIP.IsLoopback() {
			fmt.Fprintln(stderr, "TCP forward listen address is invalid")
			return 2
		}
	}
	forwards, err := parseEgressForwards(forwardValues, *port)
	if err != nil {
		fmt.Fprintln(stderr, "TCP forward configuration is invalid")
		return 2
	}

	type boundForward struct {
		listener *net.TCPListener
		target   string
	}
	bound := make([]boundForward, 0, len(forwards))
	for _, forward := range forwards {
		forwardIP := net.ParseIP(*forwardHost)
		listener, listenErr := net.ListenTCP("tcp4", &net.TCPAddr{IP: forwardIP, Port: forward.port})
		if listenErr != nil {
			for _, existing := range bound {
				_ = existing.listener.Close()
			}
			fmt.Fprintln(stderr, "cannot bind TCP forward")
			return 1
		}
		bound = append(bound, boundForward{listener: listener, target: forward.target})
	}
	defer func() {
		for _, forward := range bound {
			_ = forward.listener.Close()
		}
	}()

	var policy egress.Policy
	if err := daemon.ReadJSON(*policyPath, &policy); err != nil {
		fmt.Fprintf(stderr, "invalid egress policy: %v\n", err)
		return 2
	}
	if err := policy.Validate(); err != nil {
		fmt.Fprintf(stderr, "invalid egress policy: %v\n", err)
		return 2
	}
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: listenIP, Port: *port})
	if err != nil {
		fmt.Fprintln(stderr, "cannot bind egress proxy")
		return 1
	}
	defer listener.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	log := &audit.Log{Path: *auditPath}
	forwardDone := make(chan error, len(bound))
	for _, forward := range bound {
		forward := forward
		go func() {
			err := service.RunTCPForward(ctx, forward.listener, forward.target)
			forwardDone <- err
			if err != nil {
				cancel()
			}
		}()
	}

	err = service.RunAuthenticatedEgressProxy(
		ctx, listener, policy, *profile, authToken, log,
		func() error { return daemon.Notify(os.Getenv("NOTIFY_SOCKET"), "READY=1") },
		func(error) { fmt.Fprintln(stderr, "egress audit write failed") },
	)
	cancel()

	var forwardErr error
	for range bound {
		if err := <-forwardDone; err != nil && forwardErr == nil {
			forwardErr = err
		}
	}
	if forwardErr != nil {
		fmt.Fprintln(stderr, "TCP forward failed")
		return 1
	}
	if err != nil {
		fmt.Fprintln(stderr, "egress proxy failed")
		return 1
	}
	return 0
}
