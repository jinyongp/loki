package main

import (
	hostwindows "loki/internal/host/windows"
	"os"
)

func privateIntegrationFile(path string, _ os.FileInfo) error {
	return (hostwindows.WindowsHelperInstallPlatform{}).VerifyPrivatePath(path, false)
}
