package githubapp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"loki/internal/fault"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// UserAuthorization is host-administered. Its tokens are used only by the
// Projects command path, never as fallback credentials for repository work.
type UserAuthorization struct {
	AppID      int64
	Accounts   []string
	HTTP       *http.Client
	PrivateKey func(context.Context) (string, error)
	Load       func(context.Context) (string, error)
	Save       func(context.Context, string) error
	Now        func() time.Time

	mu       sync.Mutex
	pending  map[string]*userDeviceSession
	apiURL   string
	loginURL string
}

type UserAuthorizationView struct {
	Status          string            `json:"status"`
	Account         string            `json:"account,omitempty"`
	Accounts        []UserAccountView `json:"accounts,omitempty"`
	SessionID       string            `json:"session_id,omitempty"`
	UserCode        string            `json:"user_code,omitempty"`
	VerificationURI string            `json:"verification_uri,omitempty"`
	Interval        int               `json:"interval,omitempty"`
	ExpiresAt       time.Time         `json:"expires_at,omitzero"`
}

type UserAccountView struct {
	Account   string    `json:"account"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

type userCredential struct {
	AppID          int64     `json:"app_id"`
	ClientID       string    `json:"client_id"`
	Account        string    `json:"account"`
	AccessToken    string    `json:"access_token"`
	RefreshToken   string    `json:"refresh_token,omitempty"`
	ExpiresAt      time.Time `json:"expires_at"`
	RefreshExpires time.Time `json:"refresh_expires_at"`
}

type userDeviceSession struct {
	account, clientID, deviceCode string
	deadline, nextPoll            time.Time
	interval                      int
}

func (u *UserAuthorization) now() time.Time {
	if u.Now != nil {
		return u.Now().UTC()
	}
	return time.Now().UTC()
}

func (u *UserAuthorization) account(account string) (string, error) {
	account = strings.ToLower(strings.TrimSpace(account))
	if u == nil || u.AppID <= 0 || u.HTTP == nil || u.PrivateKey == nil || u.Load == nil || u.Save == nil || !repositoryName(account) {
		return "", fault.Error("GitHub user authorization is not configured")
	}
	for _, allowed := range u.Accounts {
		if account == allowed {
			return account, nil
		}
	}
	return "", fault.Error("GitHub personal Projects account is not allowed")
}

func (u *UserAuthorization) endpoint(path string, login bool) string {
	base := defaultAPIURL
	if u.apiURL != "" {
		base = u.apiURL
	}
	if login {
		base = "https://github.com"
		if u.loginURL != "" {
			base = u.loginURL
		}
	}
	return strings.TrimRight(base, "/") + path
}

func (u *UserAuthorization) Begin(ctx context.Context, account string) (UserAuthorizationView, error) {
	var err error
	if account != "" {
		account, err = u.account(account)
	} else if u == nil || len(u.Accounts) == 0 {
		err = fault.Error("GitHub user authorization is not configured")
	} else {
		_, err = u.account(u.Accounts[0])
	}
	if err != nil {
		return UserAuthorizationView{}, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	for id, session := range u.pending {
		if !now.Before(session.deadline) || session.account == account {
			delete(u.pending, id)
		}
	}
	if len(u.pending) >= 16 {
		return UserAuthorizationView{}, fault.Error("too many GitHub user authorization sessions")
	}
	private, err := u.PrivateKey(ctx)
	if err != nil {
		return UserAuthorizationView{}, fault.Error("GitHub App credential is unavailable")
	}
	jwt, err := AppJWT([]byte(private), u.AppID, now)
	private = ""
	if err != nil {
		return UserAuthorizationView{}, fault.Error("GitHub App authentication failed")
	}
	var app struct {
		ID       int64  `json:"id"`
		ClientID string `json:"client_id"`
	}
	err = authorizationJSON(ctx, u.HTTP, http.MethodGet, u.endpoint("/app", false), jwt, nil, &app)
	jwt = ""
	if err != nil || app.ID != u.AppID || !credentialText(app.ClientID, 100) {
		return UserAuthorizationView{}, fault.Error("GitHub App client ID is unavailable")
	}
	var device struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
		Error           string `json:"error"`
	}
	err = authorizationJSON(ctx, u.HTTP, http.MethodPost, u.endpoint("/login/device/code", true), "", url.Values{"client_id": {app.ClientID}}, &device)
	if device.Error == "device_flow_disabled" {
		return UserAuthorizationView{}, fault.Error("enable Device flow in the GitHub App settings, then retry setup")
	}
	if err != nil || device.Error != "" || !credentialText(device.DeviceCode, 4096) || !credentialText(device.UserCode, 64) ||
		device.VerificationURI != "https://github.com/login/device" || device.ExpiresIn < 1 || device.ExpiresIn > 900 || device.Interval < 1 || device.Interval > 60 {
		return UserAuthorizationView{}, fault.Error("GitHub device authorization could not start; check the App's Device flow setting")
	}
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return UserAuthorizationView{}, fault.Error("GitHub device authorization could not start")
	}
	id := hex.EncodeToString(nonce[:])
	if u.pending == nil {
		u.pending = map[string]*userDeviceSession{}
	}
	session := &userDeviceSession{account: account, clientID: app.ClientID, deviceCode: device.DeviceCode,
		deadline: now.Add(time.Duration(device.ExpiresIn) * time.Second), nextPoll: now.Add(time.Duration(device.Interval) * time.Second), interval: device.Interval}
	u.pending[id] = session
	return UserAuthorizationView{Status: "pending", Account: account, SessionID: id, UserCode: device.UserCode,
		VerificationURI: device.VerificationURI, Interval: device.Interval, ExpiresAt: session.deadline}, nil
}

type userTokenResponse struct {
	AccessToken           string `json:"access_token"`
	RefreshToken          string `json:"refresh_token"`
	ExpiresIn             int    `json:"expires_in"`
	RefreshTokenExpiresIn int    `json:"refresh_token_expires_in"`
	TokenType             string `json:"token_type"`
	Scope                 string `json:"scope"`
	Error                 string `json:"error"`
	Interval              int    `json:"interval"`
}

func (u *UserAuthorization) Poll(ctx context.Context, id string) (UserAuthorizationView, error) {
	if u == nil {
		return UserAuthorizationView{}, fault.Error("GitHub user authorization is not configured")
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	session := u.pending[id]
	if session == nil || !u.now().Before(session.deadline) {
		delete(u.pending, id)
		return UserAuthorizationView{}, fault.Error("GitHub user authorization expired; retry setup")
	}
	view := UserAuthorizationView{Status: "pending", Account: session.account, SessionID: id, Interval: session.interval, ExpiresAt: session.deadline}
	if u.now().Before(session.nextPoll) {
		return view, nil
	}
	session.nextPoll = u.now().Add(time.Duration(session.interval) * time.Second)
	var response userTokenResponse
	if err := authorizationJSON(ctx, u.HTTP, http.MethodPost, u.endpoint("/login/oauth/access_token", true), "", url.Values{
		"client_id": {session.clientID}, "device_code": {session.deviceCode}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"},
	}, &response); err != nil {
		return UserAuthorizationView{}, err
	}
	switch response.Error {
	case "authorization_pending":
		return view, nil
	case "slow_down":
		session.interval = max(session.interval+5, min(response.Interval, 900))
		session.nextPoll = u.now().Add(time.Duration(session.interval) * time.Second)
		view.Interval = session.interval
		return view, nil
	case "":
	default:
		delete(u.pending, id)
		return UserAuthorizationView{}, fault.Error("GitHub user authorization was rejected; retry setup")
	}
	delete(u.pending, id)
	credential, err := response.credential(u.AppID, session.account, session.clientID, u.now())
	if err != nil {
		return UserAuthorizationView{}, err
	}
	if session.account == "" {
		credential.Account, err = u.authorizedAccount(ctx, credential.AccessToken)
	} else {
		err = u.verifyAccount(ctx, credential)
	}
	if err != nil {
		return UserAuthorizationView{}, err
	}
	credentials, err := u.credentials(ctx)
	if err != nil {
		return UserAuthorizationView{}, err
	}
	credentials[credential.Account] = credential
	if err = u.save(ctx, credentials); err != nil {
		return UserAuthorizationView{}, err
	}
	return UserAuthorizationView{Status: "ready", Account: credential.Account, ExpiresAt: credential.ExpiresAt}, nil
}

func (r userTokenResponse) credential(appID int64, account, clientID string, now time.Time) (userCredential, error) {
	if r.Error != "" || r.TokenType != "bearer" || r.Scope != "" || !strings.HasPrefix(r.AccessToken, "ghu_") || !credentialText(r.AccessToken, 4096) ||
		r.ExpiresIn < 0 || r.ExpiresIn > 28800 || r.RefreshTokenExpiresIn < 0 || r.RefreshTokenExpiresIn > 15897600 {
		return userCredential{}, fault.Error("GitHub user token response is invalid")
	}
	c := userCredential{AppID: appID, Account: account, ClientID: clientID, AccessToken: r.AccessToken}
	if r.ExpiresIn == 0 {
		if r.RefreshToken != "" || r.RefreshTokenExpiresIn != 0 {
			return userCredential{}, fault.Error("GitHub user token response is invalid")
		}
	} else {
		if r.ExpiresIn <= int(cacheSkew.Seconds()) || !strings.HasPrefix(r.RefreshToken, "ghr_") || !credentialText(r.RefreshToken, 4096) || r.RefreshTokenExpiresIn <= r.ExpiresIn {
			return userCredential{}, fault.Error("GitHub user token response is invalid")
		}
		c.RefreshToken = r.RefreshToken
		c.ExpiresAt = now.Add(time.Duration(r.ExpiresIn) * time.Second)
		c.RefreshExpires = now.Add(time.Duration(r.RefreshTokenExpiresIn) * time.Second)
	}
	return c, nil
}

func (u *UserAuthorization) authorizedAccount(ctx context.Context, token string) (string, error) {
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Type  string `json:"type"`
	}
	if err := authorizationJSON(ctx, u.HTTP, http.MethodGet, u.endpoint("/user", false), token, nil, &user); err != nil ||
		user.ID <= 0 || user.Type != "User" {
		return "", fault.Error("authorize the configured personal account in GitHub, then retry setup")
	}
	account, err := u.account(user.Login)
	if err != nil {
		return "", fault.Error("authorize the configured personal account in GitHub, then retry setup")
	}
	return account, nil
}

func (u *UserAuthorization) verifyAccount(ctx context.Context, credential userCredential) error {
	account, err := u.authorizedAccount(ctx, credential.AccessToken)
	if err != nil || account != credential.Account {
		return fault.Error("authorize the configured personal account in GitHub, then retry setup")
	}
	return nil
}

// AccountToken accepts a personal account. It intentionally does not implement
// RepositoryTokenSource, so repository commands cannot acquire user credentials.
func (u *UserAuthorization) AccountToken(ctx context.Context, account string) (string, error) {
	account, err := u.account(account)
	if err != nil {
		return "", err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	credentials, err := u.credentials(ctx)
	if err != nil {
		return "", err
	}
	c, ok := credentials[account]
	if !ok || c.AppID != u.AppID || c.Account != account || !strings.HasPrefix(c.AccessToken, "ghu_") || !credentialText(c.AccessToken, 4096) {
		return "", fault.Error("GitHub personal Projects authorization is required; run integration setup github")
	}
	if c.ExpiresAt.IsZero() || u.now().Before(c.ExpiresAt.Add(-cacheSkew)) {
		return c.AccessToken, nil
	}
	if !credentialText(c.ClientID, 100) || !strings.HasPrefix(c.RefreshToken, "ghr_") || !credentialText(c.RefreshToken, 4096) || !u.now().Before(c.RefreshExpires) {
		return "", fault.Error("GitHub personal Projects authorization expired; run integration setup github")
	}
	var response userTokenResponse
	if err = authorizationJSON(ctx, u.HTTP, http.MethodPost, u.endpoint("/login/oauth/access_token", true), "", url.Values{
		"client_id": {c.ClientID}, "refresh_token": {c.RefreshToken}, "grant_type": {"refresh_token"},
	}, &response); err != nil {
		return "", err
	}
	rotated, err := response.credential(u.AppID, account, c.ClientID, u.now())
	if err != nil {
		return "", fault.Error("GitHub personal Projects token refresh failed; run integration setup github")
	}
	if err = u.verifyAccount(ctx, rotated); err != nil {
		return "", err
	}
	credentials[account] = rotated
	if err = u.save(ctx, credentials); err != nil {
		return "", err
	}
	return rotated.AccessToken, nil
}

func (u *UserAuthorization) Status(ctx context.Context, account string) (UserAuthorizationView, error) {
	if account == "" {
		return u.statusAll(ctx)
	}
	account, err := u.account(account)
	if err != nil {
		return UserAuthorizationView{}, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	credentials, err := u.credentials(ctx)
	if err != nil {
		return UserAuthorizationView{}, err
	}
	c, ok := credentials[account]
	view := UserAuthorizationView{Status: "unconfigured", Account: account}
	if ok && c.AppID == u.AppID && c.Account == account && strings.HasPrefix(c.AccessToken, "ghu_") && credentialText(c.AccessToken, 4096) {
		view.Status, view.ExpiresAt = "ready", c.ExpiresAt
		if !c.ExpiresAt.IsZero() && !u.now().Before(c.ExpiresAt.Add(-cacheSkew)) {
			view.Status = "refresh_required"
			if !credentialText(c.ClientID, 100) || !strings.HasPrefix(c.RefreshToken, "ghr_") || !credentialText(c.RefreshToken, 4096) || !u.now().Before(c.RefreshExpires) {
				view.Status = "expired"
			}
		}
	}
	return view, nil
}

func (u *UserAuthorization) statusAll(ctx context.Context) (UserAuthorizationView, error) {
	if u == nil {
		return UserAuthorizationView{}, fault.Error("GitHub user authorization is not configured")
	}
	view := UserAuthorizationView{Status: "not_required"}
	for _, account := range u.Accounts {
		status, err := u.Status(ctx, account)
		if err != nil {
			return UserAuthorizationView{}, err
		}
		if view.Status == "not_required" {
			view.Status = "ready"
		}
		if status.Status != "ready" && status.Status != "refresh_required" {
			view.Status = "unconfigured"
		}
		view.Accounts = append(view.Accounts, UserAccountView{Account: account, Status: status.Status, ExpiresAt: status.ExpiresAt})
	}
	return view, nil
}

func (u *UserAuthorization) Logout(ctx context.Context, account string) (UserAuthorizationView, error) {
	if account == "" {
		if u == nil || u.Save == nil {
			return UserAuthorizationView{}, fault.Error("GitHub user authorization is not configured")
		}
		u.mu.Lock()
		defer u.mu.Unlock()
		if err := u.save(ctx, map[string]userCredential{}); err != nil {
			return UserAuthorizationView{}, err
		}
		clear(u.pending)
		view := UserAuthorizationView{Status: "not_required"}
		for _, account := range u.Accounts {
			view.Status = "unconfigured"
			view.Accounts = append(view.Accounts, UserAccountView{Account: account, Status: "unconfigured"})
		}
		return view, nil
	}
	account, err := u.account(account)
	if err != nil {
		return UserAuthorizationView{}, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	credentials, err := u.credentials(ctx)
	if err != nil {
		return UserAuthorizationView{}, err
	}
	delete(credentials, account)
	for id, session := range u.pending {
		if session.account == account || session.account == "" {
			delete(u.pending, id)
		}
	}
	if err = u.save(ctx, credentials); err != nil {
		return UserAuthorizationView{}, err
	}
	return UserAuthorizationView{Status: "unconfigured", Account: account}, nil
}

func (u *UserAuthorization) credentials(ctx context.Context) (map[string]userCredential, error) {
	raw, err := u.Load(ctx)
	if err != nil {
		return nil, fault.Error("GitHub user credentials are unavailable")
	}
	credentials := map[string]userCredential{}
	if raw != "" {
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		var trailing any
		if len(raw) > 1<<20 || decoder.Decode(&credentials) != nil || credentials == nil || !errors.Is(decoder.Decode(&trailing), io.EOF) {
			return nil, fault.Error("GitHub user credentials are invalid")
		}
	}
	return credentials, nil
}

func (u *UserAuthorization) save(ctx context.Context, credentials map[string]userCredential) error {
	raw, err := json.Marshal(credentials)
	if err != nil {
		return fault.Error("GitHub user credentials could not be saved")
	}
	defer clear(raw)
	if err = u.Save(ctx, string(raw)); err != nil {
		return fault.Error("GitHub user credentials could not be saved; retry setup")
	}
	return nil
}

func credentialText(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, r := range value {
		if r <= ' ' || r > '~' {
			return false
		}
	}
	return true
}

// OAuth endpoints and API calls use fixed origins, bounded bodies, and no
// redirects. Upstream bodies and transport errors never escape into logs.
func authorizationJSON(ctx context.Context, client *http.Client, method, endpoint, token string, form url.Values, out any) error {
	var input io.Reader
	contentType := ""
	if form != nil {
		input = strings.NewReader(form.Encode())
		contentType = "application/x-www-form-urlencoded"
	}
	return authorizationBodyJSON(ctx, client, method, endpoint, token, input, contentType, out)
}

func authorizationBodyJSON(ctx context.Context, client *http.Client, method, endpoint, token string, input io.Reader, contentType string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, issuerTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, endpoint, input)
	if err != nil {
		return fault.Error("GitHub authorization request failed")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "loki")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	boundedClient := *client
	boundedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := boundedClient.Do(req)
	if err != nil {
		return fault.Error("GitHub authorization request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fault.Error("GitHub authorization request was rejected")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	defer clear(raw)
	if err != nil || len(raw) > 64<<10 {
		return fault.Error("GitHub authorization response is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var trailing any
	if decoder.Decode(out) != nil || !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return fault.Error("GitHub authorization response is invalid")
	}
	return nil
}
