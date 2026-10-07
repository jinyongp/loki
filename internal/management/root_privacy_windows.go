package management

import (
	"context"
	hostwindows "loki/internal/host/windows"
	"time"
)

func protectManagementRoot(root string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return (hostwindows.WindowsFrontendPlatform{}).EnsurePrivateDirectory(ctx, root)
}
