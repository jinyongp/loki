package main

import (
	"os"

	"loki/internal/execution"
)

const (
	defaultEgressProxyPort  = 18766
	defaultBrowserProxyPort = 18767
	defaultBrowserProxyURL  = "http://127.0.0.1:18767"
)

func loadExecutionContract(path string) (execution.Contract, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return execution.Contract{}, err
	}
	return execution.Load(raw)
}

func loadExecutionProxy(path, profile string) (string, int, error) {
	contract, err := loadExecutionContract(path)
	if err != nil {
		return "", 0, err
	}
	return contract.ProxyEndpoint(profile)
}
