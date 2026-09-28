package windows

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPHelperDownloader is platform-independent so the real HTTP size and
// cancellation boundaries are exercised without requiring a Windows runner.
// HelperManager supplies the immutable same-release URL and checks its bytes.
type HTTPHelperDownloader struct {
	Client *http.Client
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
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
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
