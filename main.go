package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

//go:embed index.html
var content embed.FS

const cookieName = "llmdash_session"

// Credentials and signing key come from the environment (Fly secrets in prod).
type config struct {
	user   string
	pass   string
	secret string
	store  Store
}

func main() {
	cfg := config{
		user:   os.Getenv("DASH_USER"),
		pass:   os.Getenv("DASH_PASS"),
		secret: os.Getenv("SESSION_SECRET"),
		store:  newStore(),
	}
	if cfg.user == "" || cfg.pass == "" {
		log.Println("WARNING: DASH_USER / DASH_PASS not set — login will reject everyone")
	}
	if cfg.secret == "" {
		cfg.secret = "dev-insecure-secret-change-me"
		log.Println("WARNING: SESSION_SECRET not set — using insecure dev secret")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", cfg.loginHandler)
	mux.HandleFunc("/api/logout", logoutHandler)
	mux.HandleFunc("/api/me", cfg.meHandler)
	mux.HandleFunc("/api/settings", cfg.settingsHandler)
	mux.HandleFunc("/api/usage", cfg.usageHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("/", staticHandler)

	log.Printf("LLM usage dashboard listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

// ---------- handlers ----------

func (c config) loginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		User string `json:"user"`
		Pass string `json:"pass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	userOK := c.user != "" && subtle.ConstantTimeCompare([]byte(body.User), []byte(c.user)) == 1
	passOK := c.pass != "" && subtle.ConstantTimeCompare([]byte(body.Pass), []byte(c.pass)) == 1
	if !userOK || !passOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid credentials"})
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    makeToken(c.user, c.secret),
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   60 * 60 * 24 * 365, // 1 year — "don't ask again"
	})
	writeJSON(w, http.StatusOK, map[string]string{"user": c.user})
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (c config) meHandler(w http.ResponseWriter, r *http.Request) {
	ck, err := r.Cookie(cookieName)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if user, ok := verifyToken(ck.Value, c.secret); ok {
		writeJSON(w, http.StatusOK, map[string]string{"user": user})
		return
	}
	w.WriteHeader(http.StatusUnauthorized)
}

func staticHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		http.NotFound(w, r)
		return
	}
	b, err := content.ReadFile("index.html")
	if err != nil {
		http.Error(w, "not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

// ---------- helpers ----------

// makeToken returns "<base64(user)>.<hmac>" signed with the session secret.
func makeToken(user, secret string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(user))
	return payload + "." + sign(payload, secret)
}

func verifyToken(token, secret string) (string, bool) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", false
	}
	if !hmac.Equal([]byte(sign(parts[0], secret)), []byte(parts[1])) {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	return string(raw), true
}

func sign(value, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// currentUser returns the authenticated user from the session cookie.
func (c config) currentUser(r *http.Request) (string, bool) {
	ck, err := r.Cookie(cookieName)
	if err != nil {
		return "", false
	}
	return verifyToken(ck.Value, c.secret)
}

// ---------- settings (stored in MongoDB Atlas) ----------

func (c config) settingsHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := c.currentUser(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	switch r.Method {
	case http.MethodGet:
		data, err := c.store.Get(ctx, user)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if data == nil {
			data = map[string]any{}
		}
		writeJSON(w, http.StatusOK, data)
	case http.MethodPost:
		var data map[string]any
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := c.store.Set(ctx, user, data); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// usageHandler pulls live usage server-side using the credentials saved in Atlas.
// Running server-side avoids the browser CORS limits the providers impose.
//
// Results are cached in MongoDB so repeated loads don't re-hit the provider API.
// Pass ?force=1 to bypass the cache. Cache TTL is USAGE_CACHE_TTL seconds (default 1h).
func (c config) usageHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := c.currentUser(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	data, err := c.store.Get(ctx, user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if data == nil {
		data = map[string]any{}
	}

	force := r.URL.Query().Get("force") == "1"
	ttl := usageCacheTTL()

	// Serve fresh cache without sending the same query again.
	if !force {
		if cached, age, ok := readCache(data, "openrouter", ttl); ok {
			writeJSON(w, http.StatusOK, map[string]any{
				"openrouter": cached, "cached": true, "ageSeconds": int(age.Seconds()),
			})
			return
		}
	}

	out := map[string]any{"cached": false}
	if key := nestedString(data, "keys", "openrouter"); key != "" {
		cost, limit, ferr := fetchOpenRouterUsage(ctx, key)
		if ferr != nil {
			// On failure, fall back to a stale cached value if we have one.
			if cached, age, ok := readCache(data, "openrouter", 0); ok {
				out["openrouter"] = cached
				out["cached"] = true
				out["stale"] = true
				out["ageSeconds"] = int(age.Seconds())
			} else {
				out["openrouter"] = nil
				out["error"] = ferr.Error()
			}
		} else {
			val := map[string]any{"cost": cost, "limit": limit}
			out["openrouter"] = val
			writeCache(data, "openrouter", val)
			if serr := c.store.Set(ctx, user, data); serr != nil {
				log.Printf("usage cache save failed: %v", serr)
			}
		}
	} else {
		out["openrouter"] = nil
	}
	writeJSON(w, http.StatusOK, out)
}

const defaultUsageCacheTTL = time.Hour

func usageCacheTTL() time.Duration {
	if s := os.Getenv("USAGE_CACHE_TTL"); s != "" {
		if secs, err := strconv.Atoi(s); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return defaultUsageCacheTTL
}

// readCache returns data["cache"][provider].value if present and (when ttl>0) fresh.
func readCache(data map[string]any, provider string, ttl time.Duration) (map[string]any, time.Duration, bool) {
	cache, ok := data["cache"].(map[string]any)
	if !ok {
		return nil, 0, false
	}
	entry, ok := cache[provider].(map[string]any)
	if !ok {
		return nil, 0, false
	}
	tsRaw, ok := entry["fetchedAt"].(string)
	if !ok {
		return nil, 0, false
	}
	ts, err := time.Parse(time.RFC3339, tsRaw)
	if err != nil {
		return nil, 0, false
	}
	val, ok := entry["value"].(map[string]any)
	if !ok {
		return nil, 0, false
	}
	age := time.Since(ts)
	if ttl > 0 && age > ttl {
		return nil, age, false
	}
	return val, age, true
}

func writeCache(data map[string]any, provider string, value map[string]any) {
	cache, ok := data["cache"].(map[string]any)
	if !ok {
		cache = map[string]any{}
		data["cache"] = cache
	}
	cache[provider] = map[string]any{
		"value":     value,
		"fetchedAt": time.Now().UTC().Format(time.RFC3339),
	}
}

// fetchOpenRouterUsage calls OpenRouter's key endpoint and returns spend + limit.
func fetchOpenRouterUsage(ctx context.Context, key string) (cost float64, limit *float64, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openrouter.ai/api/v1/key", nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, nil, fmt.Errorf("openrouter http %d", resp.StatusCode)
	}
	var out struct {
		Data struct {
			Usage float64  `json:"usage"`
			Limit *float64 `json:"limit"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, nil, err
	}
	return out.Data.Usage, out.Data.Limit, nil
}

// nestedString safely reads data[a][b] as a string.
func nestedString(data map[string]any, a, b string) string {
	if data == nil {
		return ""
	}
	inner, ok := data[a].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := inner[b].(string)
	return s
}
