package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"sbcbackend/internal/logger"
	"sbcbackend/internal/security"
)

// Request context keys
type contextKey string

const (
	RequestIDKey contextKey = "request_id"
	TokenKey     contextKey = "access_token"
	FormIDKey    contextKey = "form_id"
)

// Standard API error response
type APIError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Details   string `json:"details,omitempty"`
	RequestID string `json:"request_id"`
}

// Standard API success response
type APIResponse struct {
	Success   bool        `json:"success"`
	Data      interface{} `json:"data,omitempty"`
	RequestID string      `json:"request_id"`
}

// Rate limiting per token
var (
	tokenRateLimiter = make(map[string]time.Time)
	tokenRateMu      sync.RWMutex
	tokenRateLimit   = time.Second * 2 // 2 seconds between requests per token
)

// Middleware chain for API endpoints
func APIMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return RequestID(
		Logging(
			TokenValidation(
				TokenRateLimit(
					ErrorHandling(next),
				),
			),
		),
	)
}

// RequestID middleware adds a unique request ID to each request
func RequestID(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestID := generateRequestID()
		ctx := context.WithValue(r.Context(), RequestIDKey, requestID)
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

// Logging middleware logs all API requests with consistent format
func Logging(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		requestID := getRequestID(r.Context())

		logger.LogInfo("API request started", map[string]interface{}{
			"request_id": requestID,
			"method":     r.Method,
			"path":       r.URL.Path,
			"client_ip":  logger.GetClientIP(r),
			"user_agent": r.Header.Get("User-Agent"),
		})

		// Create a response writer that captures status code
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(rw, r)

		duration := time.Since(start)
		logger.LogInfo("API request completed", map[string]interface{}{
			"request_id":      requestID,
			"status_code":     rw.statusCode,
			"processing_time": duration.String(),
			"processing_ms":   duration.Milliseconds(),
		})
	}
}

// GetToken retrieves the access token from request context
func GetToken(ctx context.Context) string {
	if token, ok := ctx.Value(TokenKey).(string); ok {
		return token
	}
	return ""
}

// TokenValidation middleware validates access tokens for API endpoints
func TokenValidation(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-Access-Token")
		if token == "" {
			WriteAPIError(w, r, http.StatusUnauthorized, "missing_token", "Access token required", "")
			return
		}

		// Validate token format and expiration
		if !security.ValidateAccessToken(token, 30*time.Minute) {
			WriteAPIError(w, r, http.StatusUnauthorized, "invalid_token", "Access token is invalid or expired", "")
			return
		}

		// Add token to context
		ctx := context.WithValue(r.Context(), TokenKey, token)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

// TokenRateLimit implements rate limiting per access token
func TokenRateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := getToken(r.Context())
		if token == "" {
			next.ServeHTTP(w, r) // Should be caught by TokenValidation
			return
		}

		tokenRateMu.Lock()
		lastRequest, exists := tokenRateLimiter[token]
		now := time.Now()

		if exists && now.Sub(lastRequest) < tokenRateLimit {
			tokenRateMu.Unlock()
			WriteAPIError(w, r, http.StatusTooManyRequests, "rate_limit_exceeded",
				"Too many requests. Please wait before trying again.", "")
			return
		}

		tokenRateLimiter[token] = now
		tokenRateMu.Unlock()

		next.ServeHTTP(w, r)
	}
}

// ErrorHandling middleware provides panic recovery and consistent error responses
func ErrorHandling(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				requestID := getRequestID(r.Context())
				logger.LogError("Panic in API handler", map[string]interface{}{
					"request_id": requestID,
					"error":      fmt.Sprintf("%v", err),
					"path":       r.URL.Path,
					"method":     r.Method,
				})
				WriteAPIError(w, r, http.StatusInternalServerError, "internal_error",
					"An internal error occurred", "")
			}
		}()
		next.ServeHTTP(w, r)
	}
}

// Helper functions
func generateRequestID() string {
	bytes := make([]byte, 8)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

func getRequestID(ctx context.Context) string {
	if id, ok := ctx.Value(RequestIDKey).(string); ok {
		return id
	}
	return ""
}

func getToken(ctx context.Context) string {
	if token, ok := ctx.Value(TokenKey).(string); ok {
		return token
	}
	return ""
}

// WriteAPIError writes a standardized error response
func WriteAPIError(w http.ResponseWriter, r *http.Request, statusCode int, code, message, details string) {
	requestID := getRequestID(r.Context())

	response := APIError{
		Code:      code,
		Message:   message,
		Details:   details,
		RequestID: requestID,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(response)
}

// WriteAPISuccess writes a standardized success response
func WriteAPISuccess(w http.ResponseWriter, r *http.Request, data interface{}) {
	requestID := getRequestID(r.Context())

	response := APIResponse{
		Success:   true,
		Data:      data,
		RequestID: requestID,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// responseWriter wraps http.ResponseWriter to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// ParseJSONRequest parses JSON request body into the provided struct
func ParseJSONRequest(r *http.Request, v interface{}) error {
	if !strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		return fmt.Errorf("content-type must be application/json")
	}

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields() // Strict parsing
	return decoder.Decode(v)
}

// ValidateFormIDAccess validates that the token has access to the specified form ID
func ValidateFormIDAccess(ctx context.Context, formID, token string) error {
	tokenInfo := security.GetTokenInfo(token)
	if tokenInfo == nil {
		return fmt.Errorf("token not found")
	}

	if tokenInfo.FormID != formID {
		return fmt.Errorf("token does not have access to this form")
	}

	return nil
}
