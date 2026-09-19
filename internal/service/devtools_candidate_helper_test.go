package service

import (
	"fmt"
	"os"
	"testing"
)

func emitDevtoolsTestFixture(name string) {
	data, err := os.ReadFile(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if _, err := os.Stdout.Write(data); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Fprintln(os.Stdout, `{"schema_version":1,"ok":true,"data":{"version":"0.17.0","commit":"runtime-test","protocol_version":3}}`)
			os.Exit(0)
		case "schema":
			if len(os.Args) > 2 && os.Args[2] == "--all" {
				emitDevtoolsTestFixture(".loki-test-devtools-catalog.json")
			}
		case "project":
			if len(os.Args) > 2 && os.Args[2] == "inspect" {
				emitDevtoolsTestFixture(".loki-test-devtools-project.json")
			}
		case "task":
			if len(os.Args) > 2 {
				switch os.Args[2] {
				case "current":
					emitDevtoolsTestFixture(".loki-test-devtools-current.json")
				case "next":
					emitDevtoolsTestFixture(".loki-test-devtools-next.json")
				}
			}
		}
	}
	os.Exit(m.Run())
}
