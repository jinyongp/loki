package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os/signal"
	"syscall"

	appnetwork "loki/internal/app/network"
)

func runEndpointPublisher(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("endpoint-publisher", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "0.0.0.0", "TCP forward listen address")
	var forwardValues repeatedFlag
	flags.Var(&forwardValues, "forward", "repeatable LISTEN_PORT=TARGET_HOST:PORT TCP forward")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	listenIP := net.ParseIP(*host)
	if flags.NArg() != 0 || len(forwardValues) == 0 || listenIP == nil ||
		!listenIP.IsUnspecified() && !listenIP.IsLoopback() {
		fmt.Fprintln(stderr, "endpoint-publisher requires a valid listen address and at least one forward")
		return 2
	}
	forwards, err := parseEgressForwards(forwardValues, 0)
	if err != nil {
		fmt.Fprintln(stderr, "endpoint-publisher forward configuration is invalid")
		return 2
	}

	type boundForward struct {
		listener *net.TCPListener
		target   string
	}
	bound := make([]boundForward, 0, len(forwards))
	for _, forward := range forwards {
		listener, listenErr := net.ListenTCP("tcp4", &net.TCPAddr{IP: listenIP, Port: forward.port})
		if listenErr != nil {
			for _, existing := range bound {
				_ = existing.listener.Close()
			}
			fmt.Fprintln(stderr, "endpoint-publisher cannot bind TCP forward")
			return 1
		}
		bound = append(bound, boundForward{listener: listener, target: forward.target})
	}
	defer func() {
		for _, forward := range bound {
			_ = forward.listener.Close()
		}
	}()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	done := make(chan error, len(bound))
	for _, forward := range bound {
		forward := forward
		go func() {
			done <- appnetwork.RunTCPForward(ctx, forward.listener, forward.target)
		}()
	}

	firstErr := <-done
	cancel()
	for index := 1; index < len(bound); index++ {
		if err := <-done; firstErr == nil && err != nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		fmt.Fprintln(stderr, "endpoint-publisher TCP forward failed")
		return 1
	}
	return 0
}
