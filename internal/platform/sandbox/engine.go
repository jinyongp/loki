package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRequestBytes   = 256 << 10
	defaultResponseBytes  = 1 << 20
	defaultControlTimeout = 15 * time.Second
	defaultCleanupTimeout = 10 * time.Second
)

var (
	apiVersionPattern   = regexp.MustCompile(`^1\.[0-9]{1,3}$`)
	containerIDPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	containerRefPattern = regexp.MustCompile(`^(?:[0-9a-f]{64}|loki-job-[0-9a-f]{32})$`)
)

type EngineOptions struct {
	Socket         string
	ExpectedUID    *uint32
	RequestBytes   int
	ResponseBytes  int
	ControlTimeout time.Duration
	CleanupTimeout time.Duration
}

type Engine struct {
	socket         string
	expectedUID    uint32
	requestBytes   int
	responseBytes  int
	controlTimeout time.Duration
	cleanupTimeout time.Duration
	client         *http.Client
}

type Result struct {
	ExitCode int64
}

func NewEngine(options EngineOptions) (*Engine, error) {
	if !filepath.IsAbs(options.Socket) || filepath.Clean(options.Socket) != options.Socket || options.Socket == string(filepath.Separator) || strings.ContainsRune(options.Socket, 0) {
		return nil, errors.New("sandbox Docker socket must be an absolute clean path")
	}
	if options.ExpectedUID == nil {
		return nil, errors.New("sandbox Docker socket requires an expected peer UID")
	}
	requestBytes := options.RequestBytes
	if requestBytes <= 0 {
		requestBytes = defaultRequestBytes
	}
	responseBytes := options.ResponseBytes
	if responseBytes <= 0 {
		responseBytes = defaultResponseBytes
	}
	if requestBytes < 4096 || requestBytes > 4<<20 || responseBytes < 4096 || responseBytes > 16<<20 {
		return nil, errors.New("sandbox Docker protocol limits are outside the supported range")
	}
	controlTimeout := options.ControlTimeout
	if controlTimeout <= 0 {
		controlTimeout = defaultControlTimeout
	}
	cleanupTimeout := options.CleanupTimeout
	if cleanupTimeout <= 0 {
		cleanupTimeout = defaultCleanupTimeout
	}
	if controlTimeout < time.Second || controlTimeout > time.Minute || cleanupTimeout < time.Second || cleanupTimeout > time.Minute {
		return nil, errors.New("sandbox Docker timeouts are outside the supported range")
	}
	engine := &Engine{
		socket:         options.Socket,
		expectedUID:    *options.ExpectedUID,
		requestBytes:   requestBytes,
		responseBytes:  responseBytes,
		controlTimeout: controlTimeout,
		cleanupTimeout: cleanupTimeout,
	}
	transport := &http.Transport{
		DialContext:            engine.dialContext,
		DisableCompression:     true,
		DisableKeepAlives:      true,
		MaxConnsPerHost:        64,
		MaxResponseHeaderBytes: 64 << 10,
	}
	engine.client = &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("sandbox Docker daemon redirect is not allowed")
		},
	}
	return engine, nil
}

func (e *Engine) dialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", e.socket)
	if err != nil {
		return nil, err
	}
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		conn.Close()
		return nil, errors.New("sandbox Docker connection is not a Unix socket")
	}
	uid, err := unixPeerUID(unixConn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if uid != e.expectedUID {
		conn.Close()
		return nil, errors.New("sandbox Docker socket peer is not trusted")
	}
	return conn, nil
}

func (e *Engine) Run(ctx context.Context, plan Plan) (Result, error) {
	if !plan.Valid() {
		return Result{}, errors.New("sandbox workload plan is invalid")
	}
	version, err := e.apiVersion(ctx)
	if err != nil {
		return Result{}, err
	}
	containerID, created, err := e.create(ctx, version, plan)
	if err != nil {
		if created {
			return Result{}, errors.Join(err, e.cleanup(version, plan.name, false))
		}
		return Result{}, err
	}
	started := false
	cleaned := false
	defer func() {
		if !cleaned {
			_ = e.cleanup(version, containerID, started)
		}
	}()

	if err = e.start(ctx, version, containerID); err != nil {
		cleanupErr := e.cleanup(version, containerID, false)
		cleaned = true
		return Result{}, errors.Join(err, cleanupErr)
	}
	started = true

	exitCode, err := e.wait(ctx, version, containerID)
	if err != nil {
		cleanupErr := e.cleanup(version, containerID, true)
		cleaned = true
		return Result{}, errors.Join(err, cleanupErr)
	}

	cleanupErr := e.cleanup(version, containerID, false)
	cleaned = true
	if cleanupErr != nil {
		return Result{}, cleanupErr
	}
	return Result{ExitCode: exitCode}, nil
}

func (e *Engine) apiVersion(ctx context.Context) (string, error) {
	var response struct {
		APIVersion string `json:"ApiVersion"`
	}
	if err := e.controlJSON(ctx, http.MethodGet, "/version", nil, http.StatusOK, &response); err != nil {
		return "", err
	}
	if !apiVersionPattern.MatchString(response.APIVersion) {
		return "", errors.New("sandbox Docker daemon returned an invalid API version")
	}
	ok, err := apiAtLeast(response.APIVersion, 1, 41)
	if err != nil || !ok {
		return "", errors.New("sandbox Docker daemon API is too old")
	}
	return response.APIVersion, nil
}

func apiAtLeast(value string, major, minor int) (bool, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return false, errors.New("invalid API version")
	}
	gotMajor, err := strconv.Atoi(parts[0])
	if err != nil {
		return false, err
	}
	gotMinor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false, err
	}
	return gotMajor > major || gotMajor == major && gotMinor >= minor, nil
}

func (e *Engine) create(ctx context.Context, version string, plan Plan) (string, bool, error) {
	controlCtx, cancel := context.WithTimeout(ctx, e.controlTimeout)
	defer cancel()
	endpoint := "/v" + version + "/containers/create?name=" + url.QueryEscape(plan.name)
	response, err := e.call(controlCtx, http.MethodPost, endpoint, plan.create)
	if err != nil {
		return "", false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return "", false, e.unexpectedStatus(response)
	}
	raw, err := readBounded(response.Body, e.responseBytes)
	if err != nil {
		return "", true, err
	}
	var decoded struct {
		ID string `json:"Id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err = decoder.Decode(&decoded); err != nil {
		return "", true, errors.New("sandbox Docker daemon returned invalid JSON")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", true, errors.New("sandbox Docker daemon returned trailing JSON")
	}
	if !containerIDPattern.MatchString(decoded.ID) {
		return "", true, errors.New("sandbox Docker daemon returned an invalid container ID")
	}
	return decoded.ID, true, nil
}

func (e *Engine) start(ctx context.Context, version, containerID string) error {
	return e.controlJSON(ctx, http.MethodPost, "/v"+version+"/containers/"+containerID+"/start", nil, http.StatusNoContent, nil)
}

func (e *Engine) wait(ctx context.Context, version, containerID string) (int64, error) {
	var response struct {
		StatusCode int64 `json:"StatusCode"`
		Error      *struct {
			Message string `json:"Message"`
		} `json:"Error,omitempty"`
	}
	if err := e.callJSON(ctx, http.MethodPost, "/v"+version+"/containers/"+containerID+"/wait?condition=not-running", nil, http.StatusOK, &response); err != nil {
		return 0, err
	}
	if response.Error != nil && response.Error.Message != "" {
		return 0, errors.New("sandbox Docker wait reported a container error")
	}
	if response.StatusCode < 0 || response.StatusCode > 255 {
		return 0, errors.New("sandbox Docker wait returned an invalid exit code")
	}
	return response.StatusCode, nil
}

func (e *Engine) kill(ctx context.Context, version, containerRef string) error {
	if !containerRefPattern.MatchString(containerRef) {
		return errors.New("sandbox Docker container reference is invalid")
	}
	status, err := e.call(ctx, http.MethodPost, "/v"+version+"/containers/"+containerRef+"/kill?signal=KILL", nil)
	if err != nil {
		return err
	}
	defer status.Body.Close()
	if status.StatusCode == http.StatusNoContent || status.StatusCode == http.StatusNotFound || status.StatusCode == http.StatusConflict {
		_, err = readBounded(status.Body, e.responseBytes)
		return err
	}
	return e.unexpectedStatus(status)
}

func (e *Engine) remove(ctx context.Context, version, containerRef string) error {
	if !containerRefPattern.MatchString(containerRef) {
		return errors.New("sandbox Docker container reference is invalid")
	}
	status, err := e.call(ctx, http.MethodDelete, "/v"+version+"/containers/"+containerRef+"?force=1&v=1", nil)
	if err != nil {
		return err
	}
	defer status.Body.Close()
	if status.StatusCode == http.StatusNoContent || status.StatusCode == http.StatusNotFound {
		_, err = readBounded(status.Body, e.responseBytes)
		return err
	}
	return e.unexpectedStatus(status)
}

func (e *Engine) cleanup(version, containerRef string, started bool) error {
	var killErr error
	if started {
		killCtx, cancel := context.WithTimeout(context.Background(), e.cleanupTimeout)
		killErr = e.kill(killCtx, version, containerRef)
		cancel()
	}
	removeCtx, cancel := context.WithTimeout(context.Background(), e.cleanupTimeout)
	removeErr := e.remove(removeCtx, version, containerRef)
	cancel()
	return errors.Join(killErr, removeErr)
}

func (e *Engine) controlJSON(ctx context.Context, method, endpoint string, body any, want int, out any) error {
	controlCtx, cancel := context.WithTimeout(ctx, e.controlTimeout)
	defer cancel()
	return e.callJSON(controlCtx, method, endpoint, body, want, out)
}

func (e *Engine) callJSON(ctx context.Context, method, endpoint string, body any, want int, out any) error {
	response, err := e.call(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		return e.unexpectedStatus(response)
	}
	if out == nil {
		_, err = readBounded(response.Body, e.responseBytes)
		return err
	}
	raw, err := readBounded(response.Body, e.responseBytes)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err = decoder.Decode(out); err != nil {
		return errors.New("sandbox Docker daemon returned invalid JSON")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("sandbox Docker daemon returned trailing JSON")
	}
	return nil
}

func (e *Engine) call(ctx context.Context, method, endpoint string, body any) (*http.Response, error) {
	if endpoint == "" || !strings.HasPrefix(endpoint, "/") || strings.ContainsAny(endpoint, "\r\n") {
		return nil, errors.New("sandbox Docker endpoint is invalid")
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		if len(raw) > e.requestBytes {
			return nil, errors.New("sandbox Docker request exceeds limit")
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://docker"+endpoint, reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := e.client.Do(request)
	if err != nil {
		return nil, err
	}
	return response, nil
}

func (e *Engine) unexpectedStatus(response *http.Response) error {
	_, _ = readBounded(response.Body, min(e.responseBytes, 64<<10))
	return fmt.Errorf("sandbox Docker daemon returned HTTP %d", response.StatusCode)
}

func readBounded(reader io.Reader, maximum int) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, int64(maximum)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maximum {
		return nil, errors.New("sandbox Docker response exceeds limit")
	}
	return raw, nil
}
