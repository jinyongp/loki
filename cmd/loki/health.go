package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"time"
)

func runHealth(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("health", flag.ContinueOnError)
	flags.SetOutput(stderr)
	unixSocket := flags.String("unix", "", "Unix socket that must accept connections")
	tcpAddress := flags.String("tcp", "", "loopback TCP address that must accept connections")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *unixSocket == "" && *tcpAddress == "" || *unixSocket != "" && !filepath.IsAbs(*unixSocket) {
		fmt.Fprintln(stderr, "health requires an absolute Unix socket or loopback TCP address")
		return 2
	}
	addresses := [][2]string{}
	if *unixSocket != "" {
		addresses = append(addresses, [2]string{"unix", *unixSocket})
	}
	if *tcpAddress != "" {
		host, _, err := net.SplitHostPort(*tcpAddress)
		ip := net.ParseIP(host)
		if err != nil || ip == nil || !ip.IsLoopback() {
			fmt.Fprintln(stderr, "health TCP address must use an explicit loopback IP and port")
			return 2
		}
		addresses = append(addresses, [2]string{"tcp", *tcpAddress})
	}
	for _, address := range addresses {
		connection, err := net.DialTimeout(address[0], address[1], time.Second)
		if err != nil {
			return 1
		}
		connection.Close()
	}
	return 0
}
