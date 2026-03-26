package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// OAuthState contains signed state data for CSRF protection
// Shared between Slack and Webflow
type OAuthState struct {
	UserID    string `json:"u"`
	OrgID     string `json:"o"`
	Timestamp int64  `json:"t"`
	Nonce     string `json:"n"`
}

// getOAuthStateSecret returns the secret used for HMAC signing OAuth state
func getOAuthStateSecret() string {
	// Reusing SUPABASE_JWT_SECRET as the signing key for convenience
	return os.Getenv("SUPABASE_JWT_SECRET")
}

func (h *Handler) generateOAuthState(userID, orgID string) (string, error) {
	secret := getOAuthStateSecret()
	if secret == "" {
		return "", fmt.Errorf("OAuth state signing secret not configured")
	}

	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	state := OAuthState{
		UserID:    userID,
		OrgID:     orgID,
		Timestamp: time.Now().Unix(),
		Nonce:     base64.URLEncoding.EncodeToString(nonce),
	}

	data, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("failed to marshal state: %w", err)
	}

	// Sign with HMAC
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(data)
	sig := mac.Sum(nil)

	// Combine data + signature
	payload := append(data, sig...)
	return base64.URLEncoding.EncodeToString(payload), nil
}

func (h *Handler) validateOAuthState(stateParam string) (*OAuthState, error) {
	secret := getOAuthStateSecret()
	if secret == "" {
		return nil, fmt.Errorf("OAuth state secret not configured")
	}

	payload, err := base64.URLEncoding.DecodeString(stateParam)
	if err != nil {
		return nil, fmt.Errorf("invalid state encoding: %w", err)
	}

	if len(payload) < sha256.Size {
		return nil, fmt.Errorf("state too short")
	}

	data := payload[:len(payload)-sha256.Size]
	sig := payload[len(payload)-sha256.Size:]

	// Verify HMAC
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(data)
	expectedSig := mac.Sum(nil)

	if !hmac.Equal(sig, expectedSig) {
		return nil, fmt.Errorf("invalid state signature")
	}

	var state OAuthState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("invalid state data: %w", err)
	}

	// Check expiry (15 mins)
	if time.Now().Unix()-state.Timestamp > 900 {
		return nil, fmt.Errorf("state expired")
	}

	return &state, nil
}

// getAppURL returns the application URL, defaulting to production
func getAppURL() string {
	if appURL := os.Getenv("APP_URL"); appURL != "" {
		return appURL
	}
	// Production fallback for environments that haven't set APP_URL yet.
	return "https://hover.app.goodnative.co"
}

// getSettingsURL returns the settings URL
func getSettingsURL() string {
	if settingsURL := os.Getenv("SETTINGS_URL"); settingsURL != "" {
		return settingsURL
	}
	return getAppURL() + "/settings"
}

func buildSettingsURL(path string, params url.Values, fragment string) string {
	base := strings.TrimSuffix(getSettingsURL(), "/")
	if path != "" {
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		base = base + path
	}

	settingsURL, err := url.Parse(base)
	if err != nil {
		return base
	}

	if len(params) > 0 {
		settingsURL.RawQuery = params.Encode()
	}
	if fragment != "" {
		settingsURL.Fragment = fragment
	}

	return settingsURL.String()
}

func buildInviteWelcomeURL(token string) string {
	params := url.Values{}
	params.Set("invite_token", token)

	inviteURL, err := url.Parse(strings.TrimSuffix(getAppURL(), "/") + "/welcome/invite")
	if err != nil {
		return strings.TrimSuffix(getAppURL(), "/") + "/welcome/invite?invite_token=" + url.QueryEscape(token)
	}
	inviteURL.RawQuery = params.Encode()
	return inviteURL.String()
}

func (h *Handler) redirectToSettingsWithError(w http.ResponseWriter, r *http.Request, integration, errMsg, path, fragment string) {
	paramName := fmt.Sprintf("%s_error", strings.ToLower(integration))
	params := url.Values{}
	params.Set(paramName, errMsg)
	redirectURL := buildSettingsURL(path, params, fragment)
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

func (h *Handler) redirectToSettingsWithSuccess(w http.ResponseWriter, r *http.Request, integration, label, connectionID, path, fragment string) {
	paramName := fmt.Sprintf("%s_connected", strings.ToLower(integration))
	params := url.Values{}
	params.Set(paramName, label)
	if connectionID != "" {
		idParamName := fmt.Sprintf("%s_connection_id", strings.ToLower(integration))
		params.Set(idParamName, connectionID)
	}
	redirectURL := buildSettingsURL(path, params, fragment)
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

func (h *Handler) redirectToSettingsWithSetup(w http.ResponseWriter, r *http.Request, integration, label, connectionID, path, fragment string) {
	paramName := fmt.Sprintf("%s_connected", strings.ToLower(integration))
	params := url.Values{}
	params.Set(paramName, label)
	if connectionID != "" {
		idParamName := fmt.Sprintf("%s_connection_id", strings.ToLower(integration))
		params.Set(idParamName, connectionID)
	}
	setupParamName := fmt.Sprintf("%s_setup", strings.ToLower(integration))
	params.Set(setupParamName, "true")
	redirectURL := buildSettingsURL(path, params, fragment)
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}
