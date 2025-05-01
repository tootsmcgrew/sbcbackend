// internal/form/form.go
package form

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"sbcbackend/internal/data"
	"sbcbackend/internal/logger"
	"sbcbackend/internal/security"
)

var (
	timeZone           *time.Location
	recentSubmissions  = make(map[string]time.Time)
	submissionMu       sync.Mutex
	duplicateThreshold = time.Minute * 3
	rateLimiter        = make(map[string]time.Time)
	rateLimitDuration  = time.Minute
	rateLimiterMu      sync.Mutex
)

var (
	formStatsMu           sync.Mutex
	totalSubmissions      int
	successfulSubmissions int
	csrfFailures          int
	rateLimitBlocks       int
	duplicateBlocks       int
	validationFailures    int
)

func init() {
	var err error
	timeZone, err = time.LoadLocation("America/Chicago")
	if err != nil {
		log.Fatalf("Error loading time zone: %v", err)
	}
}

func logAndIncrement(stat *int, label string) {
	formStatsMu.Lock()
	*stat++
	count := *stat
	formStatsMu.Unlock()
	logger.LogInfo("Stat update: %s = %d", label, count)
}

// SubmitFormHandler processes and stores incoming form submissions
func SubmitFormHandler(w http.ResponseWriter, r *http.Request) {
	logger.LogHTTPRequest(r)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Parse form input
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		if err := r.ParseForm(); err != nil {
			logger.LogHTTPError(r, http.StatusBadRequest, err)
			http.Error(w, "Invalid form submission", http.StatusBadRequest)
			return
		}
	}

	logger.LogInfo("Form values received: %+v", r.Form)
	logAndIncrement(&totalSubmissions, "total_submissions") // at the top

	// Honeypot trap
	if r.FormValue("hidden_field") != "" {
		logger.LogWarn("Honeypot triggered by %s", logger.GetClientIP(r))
		http.Error(w, "Invalid submission", http.StatusForbidden)
		return
	}

	csrfToken := r.FormValue("csrf_token")
	if csrfToken == "" {
		err := fmt.Errorf("missing CSRF token")
		logger.LogHTTPError(r, http.StatusForbidden, err)
		logAndIncrement(&csrfFailures, "csrf_failures") // inside CSRF fail
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if !security.ValidateCSRFToken(csrfToken) {
		err := fmt.Errorf("invalid CSRF token")
		logger.LogHTTPError(r, http.StatusForbidden, err)
		logAndIncrement(&csrfFailures, "csrf_failures") // inside CSRF fail
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	clientIP := logger.GetClientIP(r)
	if isRateLimited(clientIP) {
		err := fmt.Errorf("rate limit exceeded for %s", clientIP)
		logger.LogHTTPError(r, http.StatusTooManyRequests, err)
		logAndIncrement(&rateLimitBlocks, "rate_limit_blocks") // inside rate limit block
		http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	setRateLimit(clientIP)

	formData, err := validateFormData(r)
	if err != nil {
		logger.LogHTTPError(r, http.StatusBadRequest, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	formType := formData["form_type"]
	if formType == "" {
		formType = "membership"
	}
	formID := generateFormID(formType)
	formData["formID"] = formID
	formData["submission_date"] = time.Now().Format(time.RFC3339)

	if err := data.SaveFormDataToJSON(formData, formType); err != nil {
		logger.LogHTTPError(r, http.StatusInternalServerError, err)
		http.Error(w, "Failed to save form data", http.StatusInternalServerError)
		return
	}

	submissionKey := generateSubmissionKey(formData["email"], formData["school"], formData["full_name"])
	now := time.Now()

	submissionMu.Lock()
	lastSubmit, exists := recentSubmissions[submissionKey]
	if exists && now.Sub(lastSubmit) < duplicateThreshold {
		submissionMu.Unlock()
		logger.LogWarn("Duplicate form detected for key %s", submissionKey)
		logAndIncrement(&duplicateBlocks, "duplicate_blocks") // inside duplicate check
		http.Error(w, "Duplicate detected. Please wait before submitting again.", http.StatusTooManyRequests)
		return
	}
	recentSubmissions[submissionKey] = now
	submissionMu.Unlock()

	logger.LogInfo("Form %s accepted and saved successfully", formID)
	logAndIncrement(&successfulSubmissions, "successful_submissions")
	logFormSubmissionStats(formType, r, formID)

	redirectURL := getRedirectURL(formType, formID)
	resp := map[string]string{
		"status":       "success",
		"redirect_url": redirectURL,
		"formID":       formID,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func validateFormData(r *http.Request) (map[string]string, error) {
	formData := make(map[string]string)

	for key, values := range r.Form {
		if key == "csrf_token" || key == "hidden_field" {
			continue
		}
		if len(values) > 0 {
			formData[key] = strings.TrimSpace(values[0])
		}
	}

	required := []string{"full_name", "email", "student_count"}
	for _, field := range required {
		if val := formData[field]; val == "" {
			return nil, fmt.Errorf("field '%s' is required", field)
		}
	}

	if fullName, ok := formData["full_name"]; ok {
		parts := strings.Fields(fullName)
		formData["first_name"] = parts[0]
		if len(parts) > 1 {
			formData["last_name"] = strings.Join(parts[1:], " ")
		}
	}

	if email := formData["email"]; !strings.Contains(email, "@") || !strings.Contains(email, ".") {
		return nil, fmt.Errorf("invalid email format")
	}

	if phone, ok := formData["phone"]; ok && phone != "" {
		sanitized := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, phone)
		if len(sanitized) < 10 {
			return nil, fmt.Errorf("invalid phone number")
		}
		formData["phone"] = sanitized
	}

	return formData, nil
}

func generateFormID(formType string) string {
	timestamp := data.GetCurrentTimeInZone(timeZone)
	parsedTime, err := time.Parse("2006-01-02 15:04:05 MST", timestamp)
	if err != nil {
		log.Fatalf("Error parsing timestamp: %v", err)
	}
	sanitized := parsedTime.Format("2006-01-02_15-04-05")

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	randomBytes := make([]byte, 4)
	rng.Read(randomBytes)

	token := base64.URLEncoding.EncodeToString(randomBytes)[:6]
	return fmt.Sprintf("%s-%s-%s", formType, sanitized, token)
}

func generateSubmissionKey(email, school, fullName string) string {
	base := strings.ToLower(strings.TrimSpace(email)) + "|" +
		strings.ToLower(strings.TrimSpace(school)) + "|" +
		strings.ToLower(strings.TrimSpace(fullName))
	return fmt.Sprintf("%x", sha256.Sum256([]byte(base)))
}

func getRedirectURL(formType, formID string) string {
	env := os.Getenv("ENVIRONMENT")
	base := os.Getenv("REDIRECT_BASE_URL_PROD")
	if env == "dev" {
		base = os.Getenv("REDIRECT_BASE_URL_DEV")
	}
	if base == "" {
		base = "https://suzuki.nfshost.com"
	}

	page := "thank-you.html"
	switch formType {
	case "membership":
		page = "member-checkout.html"
	case "payment":
		page = "payment-checkout.html"
	case "event":
		page = "event-checkout.html"
	}

	return fmt.Sprintf("%s/%s?formID=%s", base, page, formID)
}

func isRateLimited(ip string) bool {
	rateLimiterMu.Lock()
	defer rateLimiterMu.Unlock()
	last, ok := rateLimiter[ip]
	return ok && time.Since(last) < rateLimitDuration
}

func setRateLimit(ip string) {
	rateLimiterMu.Lock()
	defer rateLimiterMu.Unlock()
	rateLimiter[ip] = time.Now()
}

func logFormSubmissionStats(formType string, r *http.Request, formID string) {
	ip := logger.GetClientIP(r)
	timestamp := time.Now().In(timeZone).Format("2006-01-02 15:04:05")
	logger.LogInfo("Form submitted: type=%s, formID=%s, ip=%s, time=%s", formType, formID, ip, timestamp)
}
