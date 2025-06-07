// internal/security/security.go
package security

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
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

// TokenInfo stores access token metadata
type TokenInfo struct {
	FormID    string
	FormType  string
	CreatedAt time.Time
	Used      bool
}

// TokenManager handles access token lifecycle
type TokenManager struct {
	tokens map[string]*TokenInfo
	mutex  sync.RWMutex
}

// Global access token manager
var accessTokenManager = &TokenManager{
	tokens: make(map[string]*TokenInfo),
}

// Generate access token with embedded timestamp (replaces old GenerateAccessToken)
func GenerateAccessToken() (string, error) {
	// Generate random bytes
	randomBytes := make([]byte, 24)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}

	// Create timestamp (Unix timestamp as string)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	// Combine timestamp + random data
	tokenData := timestamp + ":" + base64.URLEncoding.EncodeToString(randomBytes)

	// Base64 encode the entire thing
	token := base64.URLEncoding.EncodeToString([]byte(tokenData))

	return token, nil
}

// Validate access token and check expiration
func ValidateAccessToken(token string, maxAge time.Duration) bool {
	// Decode the token
	tokenData, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return false
	}

	// Split timestamp and random data
	parts := strings.SplitN(string(tokenData), ":", 2)
	if len(parts) != 2 {
		return false
	}

	// Parse timestamp
	timestamp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return false
	}

	// Check if token has expired
	tokenTime := time.Unix(timestamp, 0)
	if time.Since(tokenTime) > maxAge {
		return false
	}

	return true
}

// Extract timestamp from access token
func GetAccessTokenAge(token string) (time.Duration, error) {
	tokenData, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return 0, err
	}

	parts := strings.SplitN(string(tokenData), ":", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid token format")
	}

	timestamp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, err
	}

	tokenTime := time.Unix(timestamp, 0)
	return time.Since(tokenTime), nil
}

// Store access token info for one-time use validation
func StoreAccessToken(token, formID, formType string) {
	accessTokenManager.mutex.Lock()
	defer accessTokenManager.mutex.Unlock()

	accessTokenManager.tokens[token] = &TokenInfo{
		FormID:    formID,
		FormType:  formType,
		CreatedAt: time.Now(),
		Used:      false,
	}
}

// Use access token (marks as used and returns info)
func UseAccessToken(token string) *TokenInfo {
	accessTokenManager.mutex.Lock()
	defer accessTokenManager.mutex.Unlock()

	info, exists := accessTokenManager.tokens[token]
	if !exists || info.Used {
		return nil
	}

	// Mark as used
	info.Used = true
	return info
}

// Validate access token with all security checks
func ValidateAndUseAccessToken(token, formID string, maxAge time.Duration) (*TokenInfo, error) {
	// Check token format and expiration
	if !ValidateAccessToken(token, maxAge) {
		age, _ := GetAccessTokenAge(token)
		if age > maxAge {
			return nil, fmt.Errorf("access link expired %v ago", age-maxAge)
		}
		return nil, fmt.Errorf("invalid access token")
	}

	// Check one-time use and get info
	tokenInfo := UseAccessToken(token)
	if tokenInfo == nil {
		return nil, fmt.Errorf("token already used or invalid")
	}

	// Verify formID matches
	if tokenInfo.FormID != formID {
		return nil, fmt.Errorf("token formID mismatch")
	}

	return tokenInfo, nil
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

// Cleanup expired access tokens
func cleanupExpiredAccessTokens(maxAge time.Duration) {
	accessTokenManager.mutex.Lock()
	defer accessTokenManager.mutex.Unlock()

	now := time.Now()
	for token, info := range accessTokenManager.tokens {
		if now.Sub(info.CreatedAt) > maxAge {
			delete(accessTokenManager.tokens, token)
		}
	}
}

// ValidateAdminToken checks if a token is a valid admin token with optional referer check
func ValidateAdminToken(token string, requireReferer bool, referer string) bool {
	// Basic token validation (format and expiration)
	if !ValidateAccessToken(token, 1*time.Hour) { // Admin tokens expire in 1 hour
		return false
	}

	// Check if token exists and is for admin access
	accessTokenManager.mutex.RLock()
	info, exists := accessTokenManager.tokens[token]
	accessTokenManager.mutex.RUnlock()

	if !exists || info.Used {
		return false
	}

	// Verify it's an admin token
	if info.FormID != "ADMIN" || info.FormType != "admin_access" {
		return false
	}

	// Optional: Check referer to ensure it came from info page
	if requireReferer && !strings.Contains(referer, "/info") {
		logger.LogWarn("Admin token used without proper referer: %s", referer)
		return false
	}

	return true
}

// CleanExpiredTokens periodically cleans up expired CSRF and access tokens.
func CleanExpiredTokens() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	tickCount := 0

	for range ticker.C {
		tickCount++

		// Clean CSRF tokens
		csrfTokensMu.Lock()
		for token, expiry := range csrfTokens {
			if time.Now().After(expiry) {
				delete(csrfTokens, token)
			}
		}
		csrfTokensMu.Unlock()

		// Clean expired access tokens (keep for 24 hours for potential logging)
		cleanupExpiredAccessTokens(24 * time.Hour)

		if tickCount%6 == 0 {
			logger.LogInfo("Token cleanup ran (CSRF and access tokens)")
		}
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

func GetTokenInfo(token string) *TokenInfo {
	accessTokenManager.mutex.RLock()
	defer accessTokenManager.mutex.RUnlock()

	if tokenInfo, exists := accessTokenManager.tokens[token]; exists {
		return tokenInfo
	}
	return nil
}

// AccessTokenInfoHandler provides debug information about access tokens
func AccessTokenInfoHandler(w http.ResponseWriter, r *http.Request) {
	logger.LogHTTPRequest(r)

	// Only accept POST requests
	if r.Method != "POST" {
		writeAPIError(w, r, http.StatusMethodNotAllowed, "method_not_allowed",
			"Only POST requests are supported", "")
		return
	}

	// Get token from request header (since middleware already validated it)
	token := r.Header.Get("X-Access-Token")
	if token == "" {
		writeAPIError(w, r, http.StatusBadRequest, "missing_token",
			"Token not found in request", "")
		return
	}

	// Get token age and validation info
	age, err := GetAccessTokenAge(token)
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "invalid_token_format",
			"Invalid token format", err.Error())
		return
	}

	maxAge := 30 * time.Minute // Match your other endpoints
	valid := ValidateAccessToken(token, maxAge)
	expired := age > maxAge

	// Get additional token info from memory
	tokenInfo := GetTokenInfo(token)
	var formID, formType string
	var createdAt *time.Time
	var used bool

	if tokenInfo != nil {
		formID = tokenInfo.FormID
		formType = tokenInfo.FormType
		createdAt = &tokenInfo.CreatedAt
		used = tokenInfo.Used
	}

	resp := map[string]interface{}{
		"valid":      valid,
		"expired":    expired,
		"age":        age.String(),
		"max_age":    maxAge.String(),
		"form_id":    formID,
		"form_type":  formType,
		"created_at": createdAt,
		"used":       used,
	}

	writeAPISuccess(w, r, resp)
}

// writeAPIError writes a standardized error response (local version to avoid import cycle)
func writeAPIError(w http.ResponseWriter, r *http.Request, statusCode int, code, message, details string) {
	response := map[string]interface{}{
		"error":   true,
		"code":    code,
		"message": message,
	}
	if details != "" {
		response["details"] = details
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(response)
}

// writeAPISuccess writes a standardized success response (local version to avoid import cycle)
func writeAPISuccess(w http.ResponseWriter, r *http.Request, data interface{}) {
	response := map[string]interface{}{
		"success": true,
		"data":    data,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
