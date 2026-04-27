package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var devClient = &http.Client{Timeout: 10 * time.Second}

// DevAutoLogin signs in as dev@example.com server-side and injects
// the session into localStorage. Sidesteps the browser→Supabase call
// that sandboxed preview browsers can't make (they reach
// localhost:8847 but not 127.0.0.1:54321). 404 outside APP_ENV=development.
// Optional ?redirect=/path query parameter (same-origin only).
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
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, //nolint:gosec // G704: authURL is from Supabase config, not user input
		authURL+"/auth/v1/token?grant_type=password", bytes.NewReader(body))
	if err != nil {
		http.Error(w, "failed to build auth request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", anonKey)

	resp, err := devClient.Do(req) //nolint:gosec // G704: request targets local Supabase auth endpoint
	if err != nil {
		http.Error(w, fmt.Sprintf("Supabase unreachable: %v\n\nRun: supabase start", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		http.Error(w, "dev login failed — is the DB seeded? Run: supabase db reset\n\n"+string(body), http.StatusBadGateway)
		return
	}

	var session map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		http.Error(w, "failed to decode auth response", http.StatusInternalServerError)
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
