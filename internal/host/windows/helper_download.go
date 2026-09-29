package windows

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"loki/internal/progress"
)

// HTTPHelperDownloader is platform-independent so the real HTTP size and
// cancellation boundaries are exercised without requiring a Windows runner.
// HelperManager supplies the immutable same-release URL and checks its bytes.
type HTTPHelperDownloader struct {
	Client   *http.Client
	Progress progress.Reporter
}

func (downloader HTTPHelperDownloader) Fetch(ctx context.Context, url string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 || maxBytes > maxRuntimeHelperBytes+1 {
		return nil, errors.New("helper download bound is invalid")
	}
	client := downloader.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("helper mirror returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maxBytes {
		return nil, errors.New("helper mirror response exceeds download bound")
	}
	reader := io.Reader(io.LimitReader(response.Body, maxBytes+1))
	if maxBytes >= 1<<20 {
		total := response.ContentLength
		if total < 0 {
			total = 0
		}
		reader = progress.NewReader(reader, downloader.Progress, progress.ReaderOptions{
			Operation: "connection", Phase: "download-helper", Label: "Connection helper", TotalBytes: total,
		})
	}
	stopHeartbeat := func() {}
	if maxBytes >= 1<<20 {
		stopHeartbeat = progress.StartHeartbeat(ctx, downloader.Progress, progress.HeartbeatOptions{
			Operation: "connection", Phase: "download-helper", Message: "Still downloading the connection helper",
		})
	}
	raw, err := io.ReadAll(reader)
	stopHeartbeat()
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxBytes {
		return nil, errors.New("helper mirror response exceeds download bound")
	}
	return raw, nil
}
