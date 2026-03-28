package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// devClient is used for the local Supabase sign-in request. Timeout prevents
// indefinite hangs if Supabase is unresponsive.
var devClient = &http.Client{Timeout: 10 * time.Second}

// DevAutoLogin signs in as the dev seed user server-side and injects the
// Supabase session directly into the browser's localStorage. This sidesteps the
// browser→Supabase network call that fails in sandboxed preview environments
// (e.g. the Claude app's embedded browser, which can reach localhost:8847 but
// not 127.0.0.1:54321 directly).
//
// Only active when APP_ENV=development. Returns 404 in all other environments.
//
// Usage: navigate to /dev/auto-login — you will be signed in as dev@example.com
// and redirected to /dashboard. An optional ?redirect=/path query parameter
// overrides the redirect target (same-origin paths only).
func (h *Handler) DevAutoLogin(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("APP_ENV") != "development" {
		http.NotFound(w, r)
		return
	}

	authURL := strings.TrimSuffix(os.Getenv("SUPABASE_AUTH_URL"), "/")
	anonKey := os.Getenv("SUPABASE_PUBLISHABLE_KEY")
	if anonKey == "" {
		anonKey = os.Getenv("SUPABASE_ANON_KEY")
	}

	// Sign in server-side — the Go process reaches 127.0.0.1:54321 fine.
	body, _ := json.Marshal(map[string]string{
		"email":    "dev@example.com",
		"password": "devpassword",
	})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		authURL+"/auth/v1/token?grant_type=password", bytes.NewReader(body))
	if err != nil {
		http.Error(w, "failed to build auth request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", anonKey)

	resp, err := devClient.Do(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("Supabase unreachable: %v\n\nRun: supabase start", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	var session map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		http.Error(w, "failed to decode auth response", http.StatusInternalServerError)
		return
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := session["msg"].(string)
		http.Error(w, "dev login failed — is the DB seeded? Run: supabase db reset\n\n"+msg, http.StatusBadGateway)
		return
	}

	// Supabase JS v2 stores sessions under: sb-{hostname.split('.')[0]}-auth-token
	parsed, err := url.Parse(authURL)
	if err != nil || parsed == nil {
		http.Error(w, "invalid SUPABASE_AUTH_URL", http.StatusInternalServerError)
		return
	}
	hostPart := strings.SplitN(parsed.Hostname(), ".", 2)[0]
	storageKey := fmt.Sprintf("sb-%s-auth-token", hostPart)

	sessionJSON, _ := json.Marshal(session)

	redirect := r.URL.Query().Get("redirect")
	if redirect == "" || !strings.HasPrefix(redirect, "/") || strings.HasPrefix(redirect, "//") {
		redirect = "/dashboard"
	}
	redirectJSON, _ := json.Marshal(redirect)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	storageKeyJSON, _ := json.Marshal(storageKey)
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Dev login…</title></head>
<body>
<script>
try {
  localStorage.setItem(%s, JSON.stringify(%s));
} catch (e) {
  document.body.textContent = 'localStorage unavailable: ' + e;
  throw e;
}
window.location.replace(%s);
</script>
<p>Signing in as dev@example.com…</p>
</body>
</html>`, storageKeyJSON, sessionJSON, redirectJSON)
}
