package main

import "errors"

type bootstrapMode uint8

const (
	bootstrapModeInstall bootstrapMode = iota + 1
	bootstrapModeUpdate
)

func parseBootstrapMode(args []string) (bootstrapMode, []string, error) {
	if len(args) == 0 {
		return 0, nil, errors.New("bootstrap action is required")
	}
	switch args[0] {
	case "install":
		return bootstrapModeInstall, args[1:], nil
	case "update":
		if len(args) != 1 {
			return 0, nil, errors.New("bootstrap update does not accept install options")
		}
		return bootstrapModeUpdate, nil, nil
	default:
		return 0, nil, errors.New("bootstrap action must be install or update")
	}
}
