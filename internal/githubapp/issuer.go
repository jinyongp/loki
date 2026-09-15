package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultAPIURL = "https://api.github.com"
	issuerTimeout = 15 * time.Second
	cacheSkew     = time.Minute
)

type IssuerConfig struct {
	AppID, InstallationID int64
	APIVersion            string
	MaxResponseBytes      int
}

type Issuer struct {
	Config     IssuerConfig
	Client     *http.Client
	PrivateKey func(context.Context) (string, error)
	Now        func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
	apiURL  string
}

func (i *Issuer) Token(ctx context.Context) (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	now := time.Now()
	if i.Now != nil {
		now = i.Now()
	}
	if i.token != "" && now.Before(i.expires.Add(-cacheSkew)) {
		return i.token, nil
	}
	if i.Config.AppID <= 0 || i.Config.InstallationID <= 0 || i.Config.APIVersion != "2022-11-28" ||
		i.Config.MaxResponseBytes < 4096 || i.Config.MaxResponseBytes > 16777216 || i.Client == nil || i.PrivateKey == nil {
		return "", errors.New("GitHub App issuer is not configured")
	}
	private, err := i.PrivateKey(ctx)
	if err != nil {
		return "", errors.New("GitHub App credential is unavailable")
	}
	key, err := parsePrivateKey(private)
	private = ""
	if err != nil {
		return "", err
	}
	jwt, err := signJWT(key, i.Config.AppID, now)
	key = nil
	if err != nil {
		return "", errors.New("GitHub App authentication failed")
	}
	base := i.apiURL
	if base == "" {
		base = defaultAPIURL
	}
	endpoint := strings.TrimRight(base, "/") + "/app/installations/" + strconv.FormatInt(i.Config.InstallationID, 10) + "/access_tokens"
	requestCtx, cancel := context.WithTimeout(ctx, issuerTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, strings.NewReader("{}"))
	if err != nil {
		return "", errors.New("GitHub App token request failed")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "loki")
	req.Header.Set("X-GitHub-Api-Version", i.Config.APIVersion)
	client := *i.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	jwt = ""
	if err != nil {
		return "", errors.New("GitHub App token request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", errors.New("GitHub App token exchange was rejected")
	}
	limited := io.LimitReader(resp.Body, int64(i.Config.MaxResponseBytes)+1)
	body, err := io.ReadAll(limited)
	if err != nil || len(body) > i.Config.MaxResponseBytes {
		return "", errors.New("GitHub App token response is invalid")
	}
	var value struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	defer clear(body)
	decoder := json.NewDecoder(bytes.NewReader(body))
	if decoder.Decode(&value) != nil || value.Token == "" || len(value.Token) > 4096 || !value.ExpiresAt.After(now.Add(cacheSkew)) {
		return "", errors.New("GitHub App token response is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", errors.New("GitHub App token response is invalid")
	}
	i.token, i.expires = value.Token, value.ExpiresAt
	return value.Token, nil
}

func signJWT(key *rsa.PrivateKey, appID int64, now time.Time) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": fmt.Sprint(appID)})
	encoded := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(encoded))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return encoded + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
