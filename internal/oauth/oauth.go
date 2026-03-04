package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	ProviderGitHub = "github"
	ProviderGoogle = "google"
)

type LoginOpts struct {
	Provider     string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	OpenBrowser  bool
	HTTPClient   *http.Client
}

type LoginResult struct {
	Provider     string
	AccountID    string
	Login        string
	Name         string
	Email        string
	AccessToken  string
	RefreshToken string
	TokenType    string
	Scope        string
	ExpiresAt    time.Time
}

type RefreshOpts struct {
	Provider     string
	ClientID     string
	ClientSecret string
	RefreshToken string
	HTTPClient   *http.Client
}

type RefreshResult struct {
	Provider     string
	AccountID    string
	Login        string
	Name         string
	Email        string
	AccessToken  string
	RefreshToken string
	TokenType    string
	Scope        string
	ExpiresAt    time.Time
}

type providerConfig struct {
	provider      string
	authURL       string
	tokenURL      string
	userinfoURL   string
	scopes        []string
	tokenExchange func(context.Context, *http.Client, providerConfig, tokenExchangeInput) (*tokenResponse, error)
	refreshToken  func(context.Context, *http.Client, providerConfig, tokenExchangeInput) (*tokenResponse, error)
	profileFetch  func(context.Context, *http.Client, providerConfig, string) (*LoginResult, error)
}

type tokenExchangeInput struct {
	code         string
	clientID     string
	clientSecret string
	redirectURL  string
	state        string
	codeVerifier string
	refreshToken string
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`

	RefreshToken          string `json:"refresh_token"`
	ExpiresIn             int    `json:"expires_in"`
	IDToken               string `json:"id_token"`
	RefreshTokenExpiresIn int    `json:"refresh_token_expires_in"`

	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

type googleDiscovery struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserInfoEndpoint      string `json:"userinfo_endpoint"`
}

type ghEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

func Login(ctx context.Context, opts LoginOpts) (*LoginResult, error) {
	if opts.ClientID == "" {
		return nil, errors.New("client id is required")
	}
	if opts.ClientSecret == "" {
		return nil, errors.New("client secret is required")
	}
	if opts.RedirectURL == "" {
		return nil, errors.New("redirect URL is required")
	}

	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}

	pCfg, err := resolveProvider(ctx, httpClient, opts.Provider)
	if err != nil {
		return nil, err
	}

	redirectURL, err := parseRedirectURL(opts.RedirectURL)
	if err != nil {
		return nil, err
	}

	state, err := randomToken(24)
	if err != nil {
		return nil, fmt.Errorf("generate state: %w", err)
	}
	verifier, err := randomToken(32)
	if err != nil {
		return nil, fmt.Errorf("generate code verifier: %w", err)
	}
	challenge := codeChallengeS256(verifier)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	srv, listener, err := startCallbackServer(redirectURL, state, codeCh, errCh)
	if err != nil {
		return nil, err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	finalRedirect := listenerRedirectURL(redirectURL, listener)
	authURL := buildAuthURL(pCfg, opts.ClientID, finalRedirect, state, challenge)

	if opts.OpenBrowser {
		if err := openInBrowser(authURL); err != nil {
			fmt.Printf("Could not open browser automatically: %v\n", err)
			fmt.Printf("Open this URL manually:\n%s\n", authURL)
		}
	} else {
		fmt.Printf("Open this URL in your browser:\n%s\n", authURL)
	}

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	tok, err := pCfg.tokenExchange(ctx, httpClient, pCfg, tokenExchangeInput{
		code:         code,
		clientID:     opts.ClientID,
		clientSecret: opts.ClientSecret,
		redirectURL:  finalRedirect,
		state:        state,
		codeVerifier: verifier,
	})
	if err != nil {
		return nil, err
	}

	result, err := pCfg.profileFetch(ctx, httpClient, pCfg, tok.AccessToken)
	if err != nil {
		return nil, err
	}
	result.Provider = pCfg.provider
	result.AccessToken = tok.AccessToken
	result.RefreshToken = tok.RefreshToken
	result.TokenType = tok.TokenType
	result.Scope = tok.Scope
	result.ExpiresAt = computeExpiry(tok.ExpiresIn)
	return result, nil
}

func Refresh(ctx context.Context, opts RefreshOpts) (*RefreshResult, error) {
	if opts.Provider == "" {
		return nil, errors.New("provider is required")
	}
	if opts.ClientID == "" {
		return nil, errors.New("client id is required")
	}
	if opts.ClientSecret == "" {
		return nil, errors.New("client secret is required")
	}
	if opts.RefreshToken == "" {
		return nil, errors.New("refresh token is required")
	}

	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}

	pCfg, err := resolveProvider(ctx, httpClient, opts.Provider)
	if err != nil {
		return nil, err
	}

	if pCfg.refreshToken == nil {
		return nil, fmt.Errorf("provider %s does not support refresh tokens", pCfg.provider)
	}

	tok, err := pCfg.refreshToken(ctx, httpClient, pCfg, tokenExchangeInput{
		clientID:     opts.ClientID,
		clientSecret: opts.ClientSecret,
		refreshToken: opts.RefreshToken,
	})
	if err != nil {
		return nil, err
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = opts.RefreshToken
	}

	profile, err := pCfg.profileFetch(ctx, httpClient, pCfg, tok.AccessToken)
	if err != nil {
		return nil, err
	}

	return &RefreshResult{
		Provider:     pCfg.provider,
		AccountID:    profile.AccountID,
		Login:        profile.Login,
		Name:         profile.Name,
		Email:        profile.Email,
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		TokenType:    tok.TokenType,
		Scope:        tok.Scope,
		ExpiresAt:    computeExpiry(tok.ExpiresIn),
	}, nil
}

func resolveProvider(ctx context.Context, httpClient *http.Client, provider string) (providerConfig, error) {
	switch strings.ToLower(provider) {
	case ProviderGitHub:
		return providerConfig{
			provider:      ProviderGitHub,
			authURL:       "https://github.com/login/oauth/authorize",
			tokenURL:      "https://github.com/login/oauth/access_token",
			userinfoURL:   "https://api.github.com/user",
			scopes:        []string{"read:user", "user:email"},
			tokenExchange: exchangeGitHubToken,
			refreshToken:  refreshGitHubToken,
			profileFetch:  fetchGitHubProfile,
		}, nil
	case ProviderGoogle:
		d, err := fetchGoogleDiscovery(ctx, httpClient)
		if err != nil {
			return providerConfig{}, err
		}
		return providerConfig{
			provider:      ProviderGoogle,
			authURL:       d.AuthorizationEndpoint,
			tokenURL:      d.TokenEndpoint,
			userinfoURL:   d.UserInfoEndpoint,
			scopes:        []string{"openid", "profile", "email"},
			tokenExchange: exchangeGoogleToken,
			refreshToken:  refreshGoogleToken,
			profileFetch:  fetchGoogleProfile,
		}, nil
	default:
		return providerConfig{}, fmt.Errorf("unsupported provider: %s", provider)
	}
}

func fetchGoogleDiscovery(ctx context.Context, httpClient *http.Client) (*googleDiscovery, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://accounts.google.com/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google discovery request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("google discovery status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var d googleDiscovery
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, fmt.Errorf("google discovery parse failed: %w", err)
	}
	if d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" || d.UserInfoEndpoint == "" {
		return nil, errors.New("google discovery response missing endpoints")
	}
	return &d, nil
}

func parseRedirectURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid redirect URL: %w", err)
	}
	if u.Scheme != "http" {
		return nil, errors.New("redirect URL must use http for loopback callback")
	}
	if u.Host == "" {
		return nil, errors.New("redirect URL must include host:port")
	}
	if u.Path == "" {
		u.Path = "/callback"
	}
	return u, nil
}

func startCallbackServer(redirectURL *url.URL, expectedState string, codeCh chan<- string, errCh chan<- error) (*http.Server, net.Listener, error) {
	mux := http.NewServeMux()
	mux.HandleFunc(redirectURL.Path, func(w http.ResponseWriter, r *http.Request) {
		if errMsg := r.URL.Query().Get("error"); errMsg != "" {
			errDesc := r.URL.Query().Get("error_description")
			http.Error(w, "OAuth authorization failed", http.StatusBadRequest)
			errCh <- fmt.Errorf("oauth authorization error: %s: %s", errMsg, errDesc)
			return
		}

		state := r.URL.Query().Get("state")
		if state == "" || state != expectedState {
			http.Error(w, "Invalid OAuth state", http.StatusBadRequest)
			errCh <- errors.New("invalid oauth state")
			return
		}

		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "Missing OAuth code", http.StatusBadRequest)
			errCh <- errors.New("oauth callback missing code")
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html><body><h3>Runic login complete</h3><p>You can close this window.</p></body></html>")
		codeCh <- code
	})

	listener, err := net.Listen("tcp", redirectURL.Host)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to listen on redirect host %s: %w", redirectURL.Host, err)
	}

	srv := &http.Server{Handler: mux}
	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	return srv, listener, nil
}

func listenerRedirectURL(redirectURL *url.URL, listener net.Listener) string {
	u := *redirectURL
	u.Host = listener.Addr().String()
	return u.String()
}

func buildAuthURL(p providerConfig, clientID, redirectURL, state, codeChallenge string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURL)
	q.Set("scope", strings.Join(p.scopes, " "))
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	if p.provider == ProviderGoogle {
		q.Set("access_type", "offline")
		q.Set("prompt", "consent")
	}
	return p.authURL + "?" + q.Encode()
}

func exchangeGitHubToken(ctx context.Context, httpClient *http.Client, p providerConfig, in tokenExchangeInput) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("client_id", in.clientID)
	form.Set("client_secret", in.clientSecret)
	form.Set("code", in.code)
	form.Set("redirect_uri", in.redirectURL)
	form.Set("state", in.state)
	form.Set("code_verifier", in.codeVerifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return doTokenRequest(httpClient, req)
}

func exchangeGoogleToken(ctx context.Context, httpClient *http.Client, p providerConfig, in tokenExchangeInput) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", in.code)
	form.Set("client_id", in.clientID)
	form.Set("client_secret", in.clientSecret)
	form.Set("redirect_uri", in.redirectURL)
	form.Set("code_verifier", in.codeVerifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return doTokenRequest(httpClient, req)
}

func refreshGitHubToken(ctx context.Context, httpClient *http.Client, p providerConfig, in tokenExchangeInput) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("client_id", in.clientID)
	form.Set("client_secret", in.clientSecret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", in.refreshToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return doTokenRequest(httpClient, req)
}

func refreshGoogleToken(ctx context.Context, httpClient *http.Client, p providerConfig, in tokenExchangeInput) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", in.clientID)
	form.Set("client_secret", in.clientSecret)
	form.Set("refresh_token", in.refreshToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return doTokenRequest(httpClient, req)
}

func doTokenRequest(httpClient *http.Client, req *http.Request) (*tokenResponse, error) {
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("token exchange response read failed: %w", err)
	}

	var tok tokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("token exchange parse failed (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode >= 400 || tok.Error != "" {
		if tok.Error == "" {
			tok.Error = strings.TrimSpace(string(body))
		}
		return nil, fmt.Errorf("token exchange failed: %s (%s)", tok.Error, tok.ErrorDescription)
	}
	if tok.AccessToken == "" {
		return nil, errors.New("token exchange did not return an access_token")
	}
	return &tok, nil
}

func fetchGitHubProfile(ctx context.Context, httpClient *http.Client, p providerConfig, accessToken string) (*LoginResult, error) {
	type ghUser struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}

	var user ghUser
	if err := doAuthedJSON(ctx, httpClient, http.MethodGet, p.userinfoURL, accessToken, map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
	}, &user); err != nil {
		return nil, err
	}

	email := user.Email
	if email == "" {
		var emails []ghEmail
		if err := doAuthedJSON(ctx, httpClient, http.MethodGet, "https://api.github.com/user/emails", accessToken, map[string]string{
			"Accept":               "application/vnd.github+json",
			"X-GitHub-Api-Version": "2022-11-28",
		}, &emails); err == nil {
			email = chooseGitHubEmail(emails)
		}
	}

	return &LoginResult{
		AccountID: fmt.Sprintf("%d", user.ID),
		Login:     user.Login,
		Name:      user.Name,
		Email:     email,
	}, nil
}

func chooseGitHubEmail(emails []ghEmail) string {
	for _, e := range emails {
		if e.Primary && e.Verified && e.Email != "" {
			return e.Email
		}
	}
	for _, e := range emails {
		if e.Verified && e.Email != "" {
			return e.Email
		}
	}
	for _, e := range emails {
		if e.Email != "" {
			return e.Email
		}
	}
	return ""
}

func fetchGoogleProfile(ctx context.Context, httpClient *http.Client, p providerConfig, accessToken string) (*LoginResult, error) {
	type googleUser struct {
		Sub           string `json:"sub"`
		Name          string `json:"name"`
		Email         string `json:"email"`
		VerifiedEmail bool   `json:"email_verified"`
	}

	var user googleUser
	if err := doAuthedJSON(ctx, httpClient, http.MethodGet, p.userinfoURL, accessToken, nil, &user); err != nil {
		return nil, err
	}

	return &LoginResult{
		AccountID: user.Sub,
		Login:     user.Email,
		Name:      user.Name,
		Email:     user.Email,
	}, nil
}

func doAuthedJSON(ctx context.Context, httpClient *http.Client, method, endpoint, accessToken string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("profile request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("profile request failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("profile parse failed: %w", err)
	}
	return nil
}

func openInBrowser(rawURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "linux":
		cmd = exec.Command("xdg-open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		return fmt.Errorf("unsupported OS for auto-open: %s", runtime.GOOS)
	}
	return cmd.Start()
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func codeChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func computeExpiry(expiresIn int) time.Time {
	if expiresIn <= 0 {
		return time.Time{}
	}
	return time.Now().UTC().Add(time.Duration(expiresIn) * time.Second)
}
