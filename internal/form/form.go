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
	"regexp"
	"strconv"
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

var emailRegex = regexp.MustCompile(`^[\p{L}0-9._%+\-]+@[\p{L}0-9.\-]+\.[\p{L}]{2,}$`)

// IsValidEmail checks whether the given email matches a reasonable pattern.
func IsValidEmail(email string) bool {
	return emailRegex.MatchString(email)
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
	logAndIncrement(&totalSubmissions, "total_submissions")

	// Honeypot trap
	if r.FormValue("hidden_field") != "" {
		logger.LogWarn("Honeypot triggered by %s", logger.GetClientIP(r))
		http.Error(w, "Invalid submission", http.StatusForbidden)
		return
	}

	csrfToken := r.FormValue("csrf_token")
	if csrfToken == "" || !security.ValidateCSRFToken(csrfToken) {
		err := fmt.Errorf("missing or invalid CSRF token")
		logger.LogHTTPError(r, http.StatusForbidden, err)
		logAndIncrement(&csrfFailures, "csrf_failures")
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	clientIP := logger.GetClientIP(r)
	if isRateLimited(clientIP) {
		err := fmt.Errorf("rate limit exceeded for %s", clientIP)
		logger.LogHTTPError(r, http.StatusTooManyRequests, err)
		logAndIncrement(&rateLimitBlocks, "rate_limit_blocks")
		http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	setRateLimit(clientIP)

	formType := r.FormValue("form_type")
	if formType == "" {
		formType = "membership"
	}

	formID := generateFormID(formType)
	submissionDate := time.Now().In(timeZone)
	accessToken, err := security.GenerateAccessToken()
	if err != nil {
		logger.LogHTTPError(r, http.StatusInternalServerError, err)
		http.Error(w, "Failed to generate access token", http.StatusInternalServerError)
		return
	}
	security.StoreAccessToken(accessToken, formID, "membership")

	submissionKey := generateSubmissionKey(r.FormValue("email"), r.FormValue("school"), r.FormValue("full_name"))
	now := time.Now()

	submissionMu.Lock()
	lastSubmit, exists := recentSubmissions[submissionKey]
	if exists && now.Sub(lastSubmit) < duplicateThreshold {
		submissionMu.Unlock()
		logger.LogWarn("Duplicate form detected for key %s", submissionKey)
		logAndIncrement(&duplicateBlocks, "duplicate_blocks")
		http.Error(w, "Duplicate detected. Please wait before submitting again.", http.StatusTooManyRequests)
		return
	}
	recentSubmissions[submissionKey] = now
	submissionMu.Unlock()

	// Unified form processing - each uses its specific parser and database function
	switch formType {
	case "membership":
		sub, err := parseMembershipSubmission(r, formID, accessToken, submissionDate)
		if err != nil {
			logger.LogHTTPError(r, http.StatusBadRequest, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := data.InsertMembership(sub); err != nil {
			logger.LogHTTPError(r, http.StatusInternalServerError, err)
			http.Error(w, "Failed to save form data", http.StatusInternalServerError)
			return
		}

	case "event":
		sub, err := parseEventSubmission(r, formID, accessToken, submissionDate)
		if err != nil {
			logger.LogHTTPError(r, http.StatusBadRequest, err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := data.InsertEvent(sub); err != nil {
			logger.LogHTTPError(r, http.StatusInternalServerError, err)
			http.Error(w, "Failed to save event form", http.StatusInternalServerError)
			return
		}

	case "fundraiser":
		handleFundraiserSubmission(w, r, formID, accessToken, submissionDate)
		return // handleFundraiserSubmission manages its own response

	default:
		http.Error(w, "Unknown form type", http.StatusBadRequest)
		return
	}

	logger.LogInfo("Form %s accepted and saved successfully", formID)
	logAndIncrement(&successfulSubmissions, "successful_submissions")
	logFormSubmissionStats(formType, r, formID)

	// Generate POST redirect to appropriate checkout page
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html := generateCheckoutRedirect(formID, accessToken, formType)
	w.Write([]byte(html))
}

func validateFormData(r *http.Request) (map[string]interface{}, error) {
	formData := make(map[string]interface{})

	for key, values := range r.Form {
		if key == "csrf_token" || key == "hidden_field" {
			continue
		}
		if len(values) > 1 {
			formData[key] = values // store slice for checkboxes (e.g., interests)
		} else if len(values) == 1 {
			formData[key] = strings.TrimSpace(values[0])
		}
	}

	// Initialize checkbox/multi-select fields with empty arrays if they don't exist
	// This ensures they're always []string instead of nil
	checkboxFields := []string{"interests", "addons"} // Add any other checkbox fields you have
	for _, field := range checkboxFields {
		if _, exists := formData[field]; !exists {
			formData[field] = []string{} // Empty slice instead of nil
		}
	}

	required := []string{"full_name", "email", "student_count"}
	for _, field := range required {
		val, ok := formData[field]
		str, isStr := val.(string)
		if !ok || (isStr && str == "") {
			return nil, fmt.Errorf("field '%s' is required", field)
		}
	}

	if fullName, ok := formData["full_name"].(string); ok {
		parts := strings.Fields(fullName)
		if len(parts) > 0 {
			formData["first_name"] = parts[0]
		}
		if len(parts) > 1 {
			formData["last_name"] = strings.Join(parts[1:], " ")
		}
	}

	email, ok := formData["email"].(string)
	if !ok || !IsValidEmail(email) {
		return nil, fmt.Errorf("invalid email format")
	}
	formData["email"] = strings.ToLower(strings.TrimSpace(email))

	if phoneVal, ok := formData["phone"]; ok {
		phone, _ := phoneVal.(string)
		if phone != "" {
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
	}

	return formData, nil
}

func generateFormID(formType string) string {
	now := time.Now().In(timeZone)
	timestamp := now.Format("2006-01-02_15-04-05")

	randomBytes := make([]byte, 4)
	rand.Read(randomBytes) // Uses crypto/rand - more secure than math/rand
	token := base64.URLEncoding.EncodeToString(randomBytes)[:6]

	return fmt.Sprintf("%s-%s-%s", formType, timestamp, token)
}

func generateSubmissionKey(email, school, fullName string) string {
	base := strings.ToLower(strings.TrimSpace(email)) + "|" +
		strings.ToLower(strings.TrimSpace(school)) + "|" +
		strings.ToLower(strings.TrimSpace(fullName))
	return fmt.Sprintf("%x", sha256.Sum256([]byte(base)))
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

// db additions

func parseMembershipSubmission(r *http.Request, formID, accessToken string, submissionDate time.Time) (data.MembershipSubmission, error) {
	fullName := r.FormValue("full_name")
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	studentCount, _ := strconv.Atoi(r.FormValue("student_count"))
	firstName, lastName := parseFirstLastName(fullName)
	interests := r.Form["interests"]
	addons := r.Form["addons"]
	if addons == nil {
		addons = []string{}
	}
	if interests == nil {
		interests = []string{}
	}
	// Parse students
	students := parseStudents(r, studentCount)
	// Parse addons (comma-separated or repeated fields)
	addons = r.Form["addons"]
	if len(addons) == 1 && strings.Contains(addons[0], ",") {
		addons = strings.Split(addons[0], ",")
	}

	sub := data.MembershipSubmission{
		FormID:           formID,
		AccessToken:      accessToken,
		SubmissionDate:   submissionDate,
		FullName:         fullName,
		FirstName:        firstName,
		LastName:         lastName,
		Email:            email,
		School:           r.FormValue("school"),
		Membership:       r.FormValue("membership"),
		MembershipStatus: r.FormValue("membership_status"),
		Describe:         r.FormValue("describe"),
		StudentCount:     studentCount,
		Students:         students,
		Addons:           addons,
		Interests:        interests,
		Donation:         parseFloatOrZero(r.FormValue("donation")),
		CalculatedAmount: parseFloatOrZero(r.FormValue("calculated_amount")),
		CoverFees:        r.FormValue("cover_fees") == "on" || r.FormValue("cover_fees") == "true",
		Submitted:        true,
		SubmittedAt:      &submissionDate,
	}
	return sub, nil
}

func parseEventSubmission(r *http.Request, formID, accessToken string, submissionDate time.Time) (data.EventSubmission, error) {
	fullName := r.FormValue("full_name")
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	studentCount, _ := strconv.Atoi(r.FormValue("student_count"))
	firstName, lastName := parseFirstLastName(fullName)
	students := parseStudents(r, studentCount)

	// --- Generalize food/lunch choices ---
	foodChoices := make(map[string]string)
	for key, vals := range r.Form {
		// Match any field related to meals, lunch, or food. Tweak as needed.
		if strings.HasPrefix(key, "meal_") || strings.Contains(key, "lunch") || strings.Contains(key, "food") {
			if len(vals) > 0 && vals[0] != "" && vals[0] != "none" && vals[0] != "0" {
				foodChoices[key] = vals[0]
			}
		}
	}
	foodChoicesJSON, err := json.Marshal(foodChoices)
	if err != nil {
		logger.LogWarn("Failed to marshal food choices: %v", err)
		foodChoicesJSON = []byte("{}")
	}

	sub := data.EventSubmission{
		FormID:         formID,
		AccessToken:    accessToken,
		SubmissionDate: submissionDate,
		Event:          r.FormValue("event"),
		FullName:       fullName,
		FirstName:      firstName,
		LastName:       lastName,
		Email:          email,
		School:         r.FormValue("school"),
		StudentCount:   studentCount,
		Students:       students,
		Submitted:      true,
		SubmittedAt:    &submissionDate,
		// NEW dynamic food fields:
		FoodChoices:     foodChoices,
		FoodChoicesJSON: string(foodChoicesJSON),
		FoodOrderID:     "",
		OrderPageURL:    "",
	}
	return sub, nil
}

// parseFundraiserSubmission parses form data into a FundraiserSubmission
func parseFundraiserSubmission(r *http.Request, formID, accessToken string, submissionDate time.Time) (data.FundraiserSubmission, error) {
	fullName := r.FormValue("full_name")
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	studentCount, _ := strconv.Atoi(r.FormValue("student_count"))
	firstName, lastName := parseFirstLastName(fullName)

	// Parse students (same as membership)
	students := parseStudents(r, studentCount)

	// Parse donation items - this is fundraiser-specific
	donationItems, totalDonation, err := parseDonationItems(r, studentCount)
	if err != nil {
		return data.FundraiserSubmission{}, fmt.Errorf("failed to parse donation items: %w", err)
	}

	// Calculate final amount with optional fee coverage
	coverFees := r.FormValue("cover_fees") == "on" || r.FormValue("cover_fees") == "true"
	calculatedAmount := totalDonation
	if coverFees {
		// Apply PayPal fee calculation (2% + $0.49)
		feeAmount := totalDonation*0.02 + 0.49
		calculatedAmount += feeAmount
	}
	// Round to 2 decimal places
	calculatedAmount = float64(int(calculatedAmount*100+0.5)) / 100

	sub := data.FundraiserSubmission{
		FormID:           formID,
		AccessToken:      accessToken,
		SubmissionDate:   submissionDate,
		FullName:         fullName,
		FirstName:        firstName,
		LastName:         lastName,
		Email:            email,
		School:           r.FormValue("school"),
		Describe:         r.FormValue("describe"),
		DonorStatus:      r.FormValue("donor_status"),
		StudentCount:     studentCount,
		Students:         students,
		DonationItems:    donationItems,
		TotalAmount:      totalDonation,
		CoverFees:        coverFees,
		CalculatedAmount: calculatedAmount,
		Submitted:        true,
		SubmittedAt:      &submissionDate,
	}

	return sub, nil
}

// parseDonationItems extracts donation amounts per student from form data
func parseDonationItems(r *http.Request, studentCount int) ([]data.StudentDonation, float64, error) {
	var donationItems []data.StudentDonation
	var totalDonation float64

	for i := 1; i <= studentCount; i++ {
		studentName := strings.TrimSpace(r.FormValue(fmt.Sprintf("student_%d_name", i)))
		amountStr := r.FormValue(fmt.Sprintf("student_%d_amount", i))

		if studentName == "" {
			continue // Skip empty student names
		}

		amount, err := strconv.ParseFloat(amountStr, 64)
		if err != nil || amount <= 0 {
			return nil, 0, fmt.Errorf("invalid donation amount for student %d (%s): %s", i, studentName, amountStr)
		}

		donationItems = append(donationItems, data.StudentDonation{
			StudentName: studentName,
			Amount:      amount,
		})

		totalDonation += amount
	}

	if len(donationItems) == 0 {
		return nil, 0, fmt.Errorf("no valid donation items found")
	}

	// Round total to 2 decimal places
	totalDonation = float64(int(totalDonation*100+0.5)) / 100

	return donationItems, totalDonation, nil
}

// validateFundraiserSubmission performs fundraiser-specific validation
func validateFundraiserSubmission(sub data.FundraiserSubmission) error {
	var errors []string

	// Basic field validation
	if sub.FullName == "" {
		errors = append(errors, "full name is required")
	}

	if sub.Email == "" {
		errors = append(errors, "email is required")
	} else if !IsValidEmail(sub.Email) {
		errors = append(errors, "invalid email format")
	}

	if sub.School == "" {
		errors = append(errors, "school is required")
	}

	if sub.Describe == "" {
		errors = append(errors, "describe field is required")
	}

	if sub.DonorStatus == "" {
		errors = append(errors, "donor status is required")
	}

	if sub.StudentCount <= 0 {
		errors = append(errors, "at least one student is required")
	}

	// Validate students match student count
	if len(sub.Students) != sub.StudentCount {
		errors = append(errors, fmt.Sprintf("student count mismatch: expected %d, got %d", sub.StudentCount, len(sub.Students)))
	}

	// Validate donation items
	if len(sub.DonationItems) == 0 {
		errors = append(errors, "at least one donation item is required")
	}

	// Validate donation amounts
	calculatedTotal := 0.0
	for i, donation := range sub.DonationItems {
		if donation.StudentName == "" {
			errors = append(errors, fmt.Sprintf("donation item %d: student name is required", i+1))
		}
		if donation.Amount <= 0 {
			errors = append(errors, fmt.Sprintf("donation item %d: amount must be greater than 0", i+1))
		}
		if donation.Amount > 1000 {
			errors = append(errors, fmt.Sprintf("donation item %d: amount exceeds maximum of $1000", i+1))
		}
		calculatedTotal += donation.Amount
	}

	// Validate total matches
	expectedTotal := float64(int(calculatedTotal*100+0.5)) / 100
	if abs(sub.TotalAmount-expectedTotal) > 0.01 {
		errors = append(errors, fmt.Sprintf("total amount mismatch: expected %.2f, got %.2f", expectedTotal, sub.TotalAmount))
	}

	// Validate calculated amount
	expectedCalculated := sub.TotalAmount
	if sub.CoverFees {
		expectedCalculated += sub.TotalAmount*0.02 + 0.49
	}
	expectedCalculated = float64(int(expectedCalculated*100+0.5)) / 100

	if abs(sub.CalculatedAmount-expectedCalculated) > 0.01 {
		errors = append(errors, fmt.Sprintf("calculated amount mismatch: expected %.2f, got %.2f", expectedCalculated, sub.CalculatedAmount))
	}

	if len(errors) > 0 {
		return fmt.Errorf("validation failed: %s", strings.Join(errors, "; "))
	}

	return nil
}

// handleFundraiserSubmission processes a complete fundraiser form submission
func handleFundraiserSubmission(w http.ResponseWriter, r *http.Request, formID, accessToken string, submissionDate time.Time) {
	// Parse the submission
	sub, err := parseFundraiserSubmission(r, formID, accessToken, submissionDate)
	if err != nil {
		logger.LogHTTPError(r, http.StatusBadRequest, err)
		http.Error(w, fmt.Sprintf("Failed to parse fundraiser submission: %v", err), http.StatusBadRequest)
		return
	}

	// Validate the submission
	if err := validateFundraiserSubmission(sub); err != nil {
		logger.LogHTTPError(r, http.StatusBadRequest, err)
		http.Error(w, fmt.Sprintf("Fundraiser validation failed: %v", err), http.StatusBadRequest)
		return
	}

	// Save to database
	if err := data.InsertFundraiser(sub); err != nil {
		logger.LogHTTPError(r, http.StatusInternalServerError, err)
		http.Error(w, "Failed to save fundraiser data", http.StatusInternalServerError)
		return
	}

	// NEW: Process payment data (equivalent to /save-payment-data for fundraisers)
	if err := data.ProcessFundraiserPayment(&sub); err != nil {
		logger.LogHTTPError(r, http.StatusInternalServerError, err)
		http.Error(w, fmt.Sprintf("Failed to process payment data: %v", err), http.StatusInternalServerError)
		return
	}

	logger.LogInfo("Fundraiser form %s processed successfully for %s (Total: $%.2f)",
		formID, sub.Email, sub.CalculatedAmount)
}

// Helper function for absolute value (since math.Abs works with float64)
func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func parseStudents(r *http.Request, count int) []data.Student {
	var students []data.Student
	for i := 1; i <= count; i++ {
		name := r.FormValue(fmt.Sprintf("student_%d_name", i))
		grade := r.FormValue(fmt.Sprintf("student_%d_grade", i))
		if name != "" {
			students = append(students, data.Student{
				Name:  name,
				Grade: grade,
			})
		}
	}
	return students
}

func parseFirstLastName(full string) (string, string) {
	parts := strings.Fields(full)
	if len(parts) == 0 {
		return "", ""
	}
	first := parts[0]
	last := ""
	if len(parts) > 1 {
		last = strings.Join(parts[1:], " ")
	}
	return first, last
}

func parseIntOrZero(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func parseFloatOrZero(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func generateCheckoutRedirect(formID, accessToken, formType string) string {
	var action, title, message string

	switch formType {
	case "membership":
		action = "/member-checkout.html"
		title = "Processing your membership..."
		message = "Please wait while we prepare your membership options."
	case "event":
		action = "/event-checkout.html"
		title = "Processing your registration..."
		message = "Please wait while we prepare your event options."
	default:
		action = "/donate.html"
		title = "Processing..."
		message = "Please wait..."
	}

	return fmt.Sprintf(`
		<!DOCTYPE html>
		<html>
		<head>
			<title>Processing...</title>
			<style>
				body { 
					font-family: system-ui, sans-serif; 
					text-align: center; 
					padding: 2rem;
					background-color: #f5f7ff;
				}
				.spinner {
					border: 4px solid #f3f3f3;
					border-top: 4px solid #663399;
					border-radius: 50%%;
					width: 40px;
					height: 40px;
					animation: spin 1s linear infinite;
					margin: 20px auto;
				}
				@keyframes spin {
					0%% { transform: rotate(0deg); }
					100%% { transform: rotate(360deg); }
				}
			</style>
		</head>
		<body>
			<h2>%s</h2>
			<div class="spinner"></div>
			<p>%s</p>
		
			<script>
			// Store data in sessionStorage (following existing pattern)
			sessionStorage.setItem('accessToken', '%s');
			sessionStorage.setItem('formID', '%s');
			
			// Navigate to checkout page
			setTimeout(function() {
				window.location.href = '%s';
			}, 2000);
			</script>
		</body>
		</html>
	`, title, message, accessToken, formID, action)
}
