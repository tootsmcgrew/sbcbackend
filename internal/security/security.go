// internal/security/security.go
package security

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"sbcbackend/internal/config"
	"sbcbackend/internal/logger"
)

var (
	csrfTokens   = make(map[string]time.Time)
	csrfTokensMu sync.Mutex
	csrfTokenTTL = time.Hour * 1
)

// making the access token for retrieving data at success.html
func GenerateAccessToken() (string, error) {
	bytes := make([]byte, 16)
	_, err := rand.Read(bytes)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(bytes), nil
}

// GenerateCSRFToken generates a new CSRF token.
func GenerateCSRFToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// Ideally, log and panic — can't securely continue if randomness fails
		panic("Failed to generate CSRF token: " + err.Error())
	}
	token := base64.StdEncoding.EncodeToString(b)

	csrfTokensMu.Lock()
	csrfTokens[token] = time.Now().Add(csrfTokenTTL)
	csrfTokensMu.Unlock()

	return token
}

// ValidateCSRFToken validates a CSRF token.
func ValidateCSRFToken(token string) bool {
	csrfTokensMu.Lock()
	defer csrfTokensMu.Unlock()

	expiry, ok := csrfTokens[token]
	if !ok || time.Now().After(expiry) {
		return false
	}
	delete(csrfTokens, token) // Consume the token
	return true
}

// CSRFTokenHandler generates and returns a CSRF token.
func CSRFTokenHandler(w http.ResponseWriter, r *http.Request) {
	logger.LogHTTPRequest(r)

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	token := GenerateCSRFToken()
	if token == "" {
		http.Redirect(w, r, "/membership.html", http.StatusFound) // Redirect on failure
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"csrf_token": token})
}

// CleanExpiredTokens periodically cleans up expired CSRF tokens.
func CleanExpiredTokens() {
	ticker := time.NewTicker(time.Minute * 5)
	defer ticker.Stop()

	for range ticker.C {
		csrfTokensMu.Lock()
		for token, expiry := range csrfTokens {
			if time.Now().After(expiry) {
				delete(csrfTokens, token)
			}
		}
		csrfTokensMu.Unlock()
		logger.LogInfo("CSRF token cleanup completed")
	}
}

// AddCORSHeaders adds CORS headers to allow requests from your frontend.
// Add this new CORS middleware function:

// CORS adds CORS headers and handles OPTIONS requests globally.
func AddCORSHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", config.AllowedOrigin) // From config
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}
