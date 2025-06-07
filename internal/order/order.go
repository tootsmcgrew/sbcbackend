// internal/order/order.go
package order

import (
	"net/http"
	"strings"
	"time"

	"sbcbackend/internal/logger"
	"sbcbackend/internal/middleware"
	"sbcbackend/internal/security"
)

/*
GetPaymentDetailsHandler is the main entry point for order details requests.
It determines the form type from the formID prefix and routes the request
to the appropriate handler (membership, event, or fundraiser).

Now uses unified JSON POST with token in X-Access-Token header.
Token validation is handled by middleware, but we provide user-friendly
error pages for expired tokens when HTML is requested.

Returns either HTML (checkout/summary pages) or JSON (API responses)
based on the Accept header.
*/
func GetPaymentDetailsHandler(w http.ResponseWriter, r *http.Request) {
	logger.LogHTTPRequest(r)

	// Only accept POST requests
	if r.Method != "POST" {
		middleware.WriteAPIError(w, r, http.StatusMethodNotAllowed, "method_not_allowed",
			"Only POST requests are supported", "")
		return
	}

	// Parse JSON request body
	var requestBody struct {
		FormID string `json:"formID"`
	}

	if err := middleware.ParseJSONRequest(r, &requestBody); err != nil {
		middleware.WriteAPIError(w, r, http.StatusBadRequest, "invalid_request",
			"Invalid JSON body", err.Error())
		return
	}

	if requestBody.FormID == "" {
		middleware.WriteAPIError(w, r, http.StatusBadRequest, "missing_form_id",
			"FormID is required", "")
		return
	}

	// Get token from middleware context
	token := middleware.GetToken(r.Context())

	// Additional token validation with user-friendly error pages
	// (Middleware already did basic validation, but we want better UX for expired tokens)
	if !security.ValidateAccessToken(token, 30*time.Minute) {
		logger.LogWarn("Expired or invalid access token from %s for formID %s", logger.GetClientIP(r), requestBody.FormID)

		// For API calls, return JSON error; for HTML requests, return user-friendly page
		acceptHeader := r.Header.Get("Accept")
		if strings.Contains(acceptHeader, "application/json") {
			middleware.WriteAPIError(w, r, http.StatusForbidden, "token_expired",
				"Access token has expired", "")
			return
		} else {
			// Return user-friendly HTML page instead of JSON error
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			html := `<!DOCTYPE html>
<html>
<head>
    <title>Session Expired</title>
    <link rel="stylesheet" href="/static/css/simple.css">
</head>
<body>
    <main>
        <h1>Session Expired</h1>
        <p>Your session has expired for security reasons. Sessions are limited to 30 minutes to protect your personal information and payment data.</p>
        <p>Please return to the homepage and begin the registration process again.</p>
        <a href="/" class="button">Return to Homepage</a>
    </main>
</body>
</html>`
			w.Write([]byte(html))
			return
		}
	}

	// Validate token access to this specific form
	if err := middleware.ValidateFormIDAccess(r.Context(), requestBody.FormID, token); err != nil {
		logger.LogWarn("FormID access denied for token from %s", logger.GetClientIP(r))

		// Also provide user-friendly error for access denied
		acceptHeader := r.Header.Get("Accept")
		if strings.Contains(acceptHeader, "application/json") {
			middleware.WriteAPIError(w, r, http.StatusForbidden, "access_denied",
				"Access denied to this form", "")
		} else {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			html := `<!DOCTYPE html>
<html>
<head>
    <title>Access Denied</title>
    <link rel="stylesheet" href="/static/css/simple.css">
</head>
<body>
    <main>
        <h1>Access Denied</h1>
        <p>You don't have permission to access this form.</p>
        <p>Please return to the homepage and begin the registration process again.</p>
        <a href="/" class="button">Return to Homepage</a>
    </main>
</body>
</html>`
			w.Write([]byte(html))
		}
		return
	}

	// Determine form type from formID prefix
	formType := getFormTypeFromID(requestBody.FormID)

	switch formType {
	case "membership":
		handleMembershipOrderDetails(w, r, requestBody.FormID, token)
	case "fundraiser":
		handleFundraiserOrderDetails(w, r, requestBody.FormID, token)
	case "event":
		handleEventOrderDetails(w, r, requestBody.FormID, token)
	default:
		logger.LogError("Unknown form type for formID %s", requestBody.FormID)
		middleware.WriteAPIError(w, r, http.StatusBadRequest, "unknown_form_type",
			"Unknown form type", "")
	}
}

/*
GetSuccessPageHandler is the main entry point for success/receipt page requests.
It determines the form type from the formID prefix and routes the request
to the appropriate success page handler (membership, event, or fundraiser).

Now uses unified JSON POST with token in X-Access-Token header.
Token validation is handled by middleware.

Handles both regular user access (with access tokens) and admin access
(with admin tokens via query parameter).

For completed payments, implements database token fallback to handle cases
where the in-memory token has expired but payment was successfully processed.
*/
func GetSuccessPageHandler(w http.ResponseWriter, r *http.Request) {
	logger.LogHTTPRequest(r)

	// Only accept POST requests
	if r.Method != "POST" {
		middleware.WriteAPIError(w, r, http.StatusMethodNotAllowed, "method_not_allowed",
			"Only POST requests are supported", "")
		return
	}

	// Check for admin token access (still via query parameter)
	adminToken := r.URL.Query().Get("adminToken")
	isAdminView := adminToken != ""

	var formID string
	var token string

	if isAdminView {
		// Admin access - get formID from query parameter
		formID = r.URL.Query().Get("formID")
		token = adminToken // Use admin token for validation
	} else {
		// Regular user access - parse JSON request body
		var requestBody struct {
			FormID string `json:"formID"`
		}

		if err := middleware.ParseJSONRequest(r, &requestBody); err != nil {
			middleware.WriteAPIError(w, r, http.StatusBadRequest, "invalid_request",
				"Invalid JSON body", err.Error())
			return
		}

		formID = requestBody.FormID
		token = middleware.GetToken(r.Context()) // Get token from middleware context
	}

	if formID == "" {
		logger.LogWarn("Success page accessed without formID from %s", logger.GetClientIP(r))
		middleware.WriteAPIError(w, r, http.StatusBadRequest, "missing_form_id",
			"FormID is required", "")
		return
	}

	// Dispatch by form type (first part before "-")
	formType := getFormTypeFromID(formID)
	switch formType {
	case "membership":
		handleMembershipSuccessPage(w, r, formID, token, isAdminView, adminToken)
	case "fundraiser":
		handleFundraiserSuccessPage(w, r, formID, token, isAdminView, adminToken)
	case "event":
		handleEventSuccessPage(w, r, formID, token, isAdminView, adminToken)
	default:
		logger.LogError("Unknown form type for formID %s", formID)
		middleware.WriteAPIError(w, r, http.StatusBadRequest, "unknown_form_type",
			"Unknown form type", "")
		return
	}
}
