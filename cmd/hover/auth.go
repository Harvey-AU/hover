package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	defaultAuthURL           = "https://hover.auth.goodnative.co"
	defaultAnonKey           = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzdXBhYmFzZSIsInJlZiI6Imdwemp0Ymd0ZGp4bmFjZGZ1anZ4Iiwicm9sZSI6ImFub24iLCJpYXQiOjE3NDUwNjYxNjMsImV4cCI6MjA2MDY0MjE2M30.eJjM2-3X8oXsFex_lQKvFkP1-_yLMHsueIn7_hCF6YI"
	tokenSkewSeconds         = 90  // interactive: refresh within 90s of expiry
	silentRefreshSkewSeconds = 600 // mid-run: start refreshing 10 min before expiry
	callbackTimeout          = 5 * time.Minute
	callbackPort             = 8765
	supabaseTokenPath        = "/auth/v1/token" //nolint:gosec // URL path, not a credential
)

// session represents a cached Supabase auth session.
type session struct {
	AccessToken  string          `json:"access_token"`
	RefreshToken string          `json:"refresh_token"`
	ExpiresIn    float64         `json:"expires_in"`
	ExpiresAt    float64         `json:"expires_at,omitempty"`
	FetchedAt    float64         `json:"fetched_at"`
	TokenType    string          `json:"token_type,omitempty"`
	User         json.RawMessage `json:"user,omitempty"`
}

func (s *session) isValid() bool {
	return s.isValidWithSkew(tokenSkewSeconds)
}

// isValidForSilent uses a larger skew so background refresh starts 10 min
// before expiry, giving retries room to succeed without browser interaction.
func (s *session) isValidForSilent() bool {
	return s.isValidWithSkew(silentRefreshSkewSeconds)
}

func (s *session) isValidWithSkew(skewSeconds float64) bool {
	expiresAt := s.ExpiresAt
	if expiresAt == 0 && s.ExpiresIn > 0 && s.FetchedAt > 0 {
		expiresAt = s.FetchedAt + s.ExpiresIn
	}
	if expiresAt == 0 {
		return false
	}
	return expiresAt-skewSeconds > float64(time.Now().Unix())
}

// authConfig holds resolved auth parameters for a CLI invocation.
type authConfig struct {
	AuthURL string
	AnonKey string
	APIURL  string
	PR      int
}

func (c *authConfig) sessionFile() string {
	dir := configDir()
	// Scope session to the auth target so different Supabase projects
	// (e.g. production vs preview) never share a cached token.
	h := sha256.Sum256([]byte(c.AuthURL))
	suffix := hex.EncodeToString(h[:4])
	if c.PR > 0 {
		return filepath.Join(dir, fmt.Sprintf("session-pr-%d-%s.json", c.PR, suffix))
	}
	return filepath.Join(dir, fmt.Sprintf("session-%s.json", suffix))
}

func configDir() string {
	if v := os.Getenv("BBB_AUTH_DIR"); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "Hover", "auth")
		}
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "AppData", "Roaming", "Hover", "auth")
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "hover", "auth")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "hover", "auth")
}

func loadSession(path string) (*session, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is from configDir(), not user input
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("invalid session file: %w", err)
	}
	return &s, nil
}

func saveSession(s *session, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ") //nolint:gosec // G117: session file written with 0600 perms to user config dir
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ensureTokenSilent returns a valid access token using only cached credentials.
// Unlike ensureToken it never opens a browser, so it is safe to call mid-run.
// Returns an error if the token cannot be refreshed without user interaction.
func ensureTokenSilent(ctx context.Context, cfg *authConfig) (string, error) {
	sf := cfg.sessionFile()
	sess, err := loadSession(sf)
	if err != nil {
		return "", fmt.Errorf("could not read session: %w", err)
	}
	if sess == nil {
		return "", fmt.Errorf("no cached session")
	}
	if sess.isValidForSilent() {
		return sess.AccessToken, nil
	}
	if sess.RefreshToken == "" {
		return "", fmt.Errorf("session expired and no refresh token available")
	}
	refreshed, err := refreshSession(ctx, cfg, sess.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("token refresh failed: %w", err)
	}
	if err := saveSession(refreshed, sf); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not cache refreshed session: %v\n", err)
	}
	return refreshed.AccessToken, nil
}

// ensureToken returns a valid access token, refreshing or performing a
// browser login as needed.
func ensureToken(ctx context.Context, cfg *authConfig) (string, error) {
	sf := cfg.sessionFile()
	sess, err := loadSession(sf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not read session: %v\n", err)
	}

	// Reuse cached token if still valid.
	if sess != nil && sess.isValid() {
		return sess.AccessToken, nil
	}

	// Attempt refresh if we have a refresh token.
	if sess != nil && sess.RefreshToken != "" {
		refreshed, err := refreshSession(ctx, cfg, sess.RefreshToken)
		if err == nil {
			if err := saveSession(refreshed, sf); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not cache session: %v\n", err)
			}
			return refreshed.AccessToken, nil
		}
		fmt.Fprintf(os.Stderr, "Session refresh failed: %v\n", err)
	}

	// Fall back to browser login.
	fmt.Fprintln(os.Stderr, "No valid session found. Starting browser login...")
	newSess, err := browserLogin(ctx, cfg)
	if err != nil {
		return "", fmt.Errorf("browser login failed: %w", err)
	}
	if err := saveSession(newSess, sf); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not cache session: %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "Session saved to %s\n", sf)
	return newSess.AccessToken, nil
}

// refreshSession exchanges a refresh token for a new session.
func refreshSession(ctx context.Context, cfg *authConfig, refreshToken string) (*session, error) {
	tokenURL := cfg.AuthURL + supabaseTokenPath + "?grant_type=refresh_token"
	payload := fmt.Sprintf(`{"refresh_token":%q}`, refreshToken)

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", cfg.AnonKey)
	req.Header.Set("Authorization", "Bearer "+cfg.AnonKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh failed (HTTP %d): %s", resp.StatusCode, body)
	}

	var s session
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("invalid refresh response: %w", err)
	}
	s.FetchedAt = float64(time.Now().Unix())
	return &s, nil
}

// browserLogin opens the app's existing auth page in the browser with a
// cli_callback parameter. After the user signs in using the app's normal
// auth flow (email, Google, GitHub — whatever the app supports), a small
// hook in auth.js detects the callback and POSTs the session to our
// loopback server.
func browserLogin(ctx context.Context, cfg *authConfig) (*session, error) {
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", callbackPort))
	if err != nil {
		return nil, fmt.Errorf("could not bind 127.0.0.1:%d: %w", callbackPort, err)
	}

	sessCh := make(chan *session, 1)
	errCh := make(chan error, 1)

	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/callback", callbackPort)

	mux := http.NewServeMux()

	// CORS preflight — the app page needs to POST cross-origin to localhost.
	handleCORS := func(w http.ResponseWriter, r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Header.Get("Access-Control-Request-Private-Network") == "true" {
			w.Header().Set("Access-Control-Allow-Private-Network", "true")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return true
		}
		return false
	}

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if handleCORS(w, r) {
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)

		// Accept both {session: {...}} and flat session payloads.
		var wrapper struct {
			Session json.RawMessage `json:"session"`
		}
		var raw json.RawMessage
		if err := json.Unmarshal(body, &wrapper); err == nil && len(wrapper.Session) > 0 {
			raw = wrapper.Session
		} else {
			raw = body
		}

		var s session
		if err := json.Unmarshal(raw, &s); err != nil || s.AccessToken == "" {
			http.Error(w, "invalid session", http.StatusBadRequest)
			return
		}
		s.FetchedAt = float64(time.Now().Unix())

		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<html><body><h2>Authentication complete</h2><p>You can close this tab and return to your terminal.</p></body></html>`)
		sessCh <- &s
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second} //nolint:gosec // loopback only

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	defer func() {
		_ = srv.Shutdown(context.Background())
		wg.Wait()
	}()

	// Open the app's homepage with the cli_callback param. The app's auth.js
	// detects this and sends the session back after successful authentication.
	loginURL := cfg.APIURL + "/?cli_callback=" + url.QueryEscape(callbackURL)
	fmt.Fprintln(os.Stderr, "Opening browser for authentication...")
	fmt.Fprintf(os.Stderr, "If your browser does not open, visit:\n  %s\n\n", loginURL)
	openBrowser(loginURL)

	select {
	case s := <-sessCh:
		return s, nil
	case err := <-errCh:
		return nil, err
	case <-time.After(callbackTimeout):
		return nil, fmt.Errorf("timed out waiting for authentication (waited %s)", callbackTimeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// openBrowser tries to open a URL in the default browser.
// Only HTTP(S) URLs are allowed to prevent command injection via crafted schemes.
func openBrowser(rawURL string) {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", parsed.String()) //nolint:gosec // G204: scheme validated as http(s) above
	case "linux":
		cmd = exec.Command("xdg-open", parsed.String()) //nolint:gosec // G204: scheme validated as http(s) above
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", parsed.String()) //nolint:gosec // G204: scheme validated as http(s) above
	default:
		return
	}
	cmd.Start() //nolint:errcheck,gosec // best-effort browser open
}

// orgInfo represents an organisation returned by the API.
type orgInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// identity holds the resolved user name, org list, and active org.
type identity struct {
	UserName    string
	Orgs        []orgInfo
	ActiveOrgID string
}

// ActiveOrgName returns the name of the active organisation.
func (id *identity) ActiveOrgName() string {
	for _, org := range id.Orgs {
		if org.ID == id.ActiveOrgID {
			return org.Name
		}
	}
	return ""
}

// fetchIdentity retrieves the user's name from the cached session and
// their organisations from the API.
func fetchIdentity(ctx context.Context, cfg *authConfig, token string) *identity {
	id := &identity{}

	// Extract user name from the cached session.
	sf := cfg.sessionFile()
	sess, err := loadSession(sf)
	if err == nil && sess != nil && len(sess.User) > 0 {
		var user struct {
			UserMetadata struct {
				FullName string `json:"full_name"`
				Name     string `json:"name"`
			} `json:"user_metadata"`
			Email string `json:"email"`
		}
		if json.Unmarshal(sess.User, &user) == nil {
			id.UserName = user.UserMetadata.FullName
			if id.UserName == "" {
				id.UserName = user.UserMetadata.Name
			}
		}
	}

	// Fetch organisations from the API.
	req, err := http.NewRequestWithContext(ctx, "GET", cfg.APIURL+"/v1/organisations", nil)
	if err != nil {
		return id
	}
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return id
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return id
	}

	var result struct {
		Data struct {
			Organisations []orgInfo `json:"organisations"`
			ActiveOrgID   string    `json:"active_organisation_id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return id
	}
	id.Orgs = result.Data.Organisations
	id.ActiveOrgID = result.Data.ActiveOrgID
	return id
}

// switchOrg calls POST /v1/organisations/switch to change the active org.
func switchOrg(ctx context.Context, cfg *authConfig, token, orgID string) error {
	payload := fmt.Sprintf(`{"organisation_id":%q}`, orgID)
	req, err := http.NewRequestWithContext(ctx, "POST", cfg.APIURL+"/v1/organisations/switch", strings.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("switch org failed (HTTP %d)", resp.StatusCode)
	}
	return nil
}

// discoveredConfig holds auth config fetched from a running app's /config.js.
type discoveredConfig struct {
	authURL string
	anonKey string
}

var (
	reSupabaseURL = regexp.MustCompile(`"supabaseUrl"\s*:\s*"([^"]+)"`)
	reAnonKey     = regexp.MustCompile(`"supabaseAnonKey"\s*:\s*"([^"]+)"`)
)

// discoverConfig fetches /config.js from the target API and extracts the
// Supabase URL and anon key so preview PRs automatically use the correct
// Supabase project.
func discoverConfig(apiURL string) discoveredConfig {
	var dc discoveredConfig
	configURL := strings.TrimSuffix(apiURL, "/") + "/config.js"
	fmt.Fprintf(os.Stderr, "Discovering auth config from %s...\n", configURL)

	req, err := http.NewRequest("GET", configURL, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not build config request: %v\n", err)
		return dc
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not fetch config: %v\n", err)
		return dc
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Warning: config endpoint returned HTTP %d\n", resp.StatusCode)
		return dc
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not read config response: %v\n", err)
		return dc
	}

	if m := reSupabaseURL.FindSubmatch(body); len(m) > 1 {
		dc.authURL = string(m[1])
	}
	if m := reAnonKey.FindSubmatch(body); len(m) > 1 {
		dc.anonKey = string(m[1])
	}

	if dc.authURL != "" {
		fmt.Fprintf(os.Stderr, "Discovered auth URL: %s\n", dc.authURL)
	}
	return dc
}
