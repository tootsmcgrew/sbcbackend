// internal/order/fundraiser.go
package order

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"sbcbackend/internal/data"
	"sbcbackend/internal/email"
	"sbcbackend/internal/logger"
	"sbcbackend/internal/security"
)

// Variables

// Types

/*
checkout flow code goes below
from front to back
*/

// parsing, processing

// handleFundraiserOrderDetails processes fundraiser order details
func handleFundraiserOrderDetails(w http.ResponseWriter, r *http.Request, formID, token string) {
	sub, err := data.GetFundraiserByID(formID)
	if err != nil {
		logger.LogError("GetFundraiserByID failed for %s: %v", formID, err)
		http.Error(w, "Fundraiser details not found", http.StatusNotFound)
		return
	}

	// Validate access token matches
	if sub.AccessToken != token {
		logger.LogWarn("Access token mismatch for formID %s from %s", formID, logger.GetClientIP(r))
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Compose the struct for template
	resp := struct {
		FormID           string
		FormType         string
		FullName         string
		FirstName        string
		LastName         string
		Email            string
		School           string
		Describe         string
		DonorStatus      string
		StudentCount     int
		Students         []data.Student
		DonationItems    []data.StudentDonation
		TotalAmount      float64
		CalculatedAmount float64
		CoverFees        bool
		ProcessingFee    float64
		SubmittedAt      *time.Time
	}{
		FormID:           sub.FormID,
		FormType:         "fundraiser",
		FullName:         sub.FullName,
		FirstName:        sub.FirstName,
		LastName:         sub.LastName,
		Email:            sub.Email,
		School:           formatDisplayName(sub.School),
		Describe:         formatDisplayName(sub.Describe),
		DonorStatus:      formatDisplayName(sub.DonorStatus),
		StudentCount:     sub.StudentCount,
		Students:         sub.Students,
		DonationItems:    sub.DonationItems,
		TotalAmount:      sub.TotalAmount,
		CalculatedAmount: sub.CalculatedAmount,
		ProcessingFee:    sub.CalculatedAmount - sub.TotalAmount,
		CoverFees:        sub.CoverFees,
		SubmittedAt:      sub.SubmittedAt,
	}

	logger.LogInfo("Fundraiser order details accessed for form %s", formID)

	// Render template or return JSON based on Accept header
	acceptHeader := r.Header.Get("Accept")
	if strings.Contains(acceptHeader, "text/html") || strings.HasSuffix(r.URL.Path, ".html") {
		// Render as HTML using the fundraiser template
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := fundraiserSummaryTmpl.Execute(w, resp); err != nil {
			logger.LogError("Failed to render fundraiser summary template: %v", err)
			http.Error(w, "Error rendering page", http.StatusInternalServerError)
		}
		return
	}

	// Return JSON (for API calls)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// summary pages

// success pages

func handleFundraiserSuccessPage(w http.ResponseWriter, r *http.Request, formID, token string, isAdminView bool, adminToken string) {
	// Admin check (if you have one - skip if not)
	if isAdminView {
		// ... keep any existing admin validation code
	}

	// 1. Load submission first
	sub, err := data.GetFundraiserByID(formID)
	if err != nil {
		logger.LogError("GetFundraiserByID failed for %s: %v", formID, err)
		http.Error(w, "Order details not found", http.StatusNotFound)
		return
	}

	// 2. Token validation with database fallback
	if !isAdminView {
		if token == "" {
			logger.LogWarn("Fundraiser success page accessed without token from %s", logger.GetClientIP(r))
			showTokenExpiredPage(w, "fundraiser")
			return
		}

		tokenInfo := security.GetTokenInfo(token)

		if tokenInfo == nil {
			// Fallback: For completed payments, check against database token
			if sub.PayPalStatus == "COMPLETED" && sub.AccessToken == token {
				logger.LogInfo("Using database token validation for completed fundraiser payment %s (server restart recovery)", formID)
			} else {
				logger.LogWarn("Token not found in memory and payment not completed for fundraiser %s from %s", formID, logger.GetClientIP(r))
				showTokenExpiredPage(w, "fundraiser")
				return
			}
		} else {
			// Token found in memory - use normal validation flow
			if !security.ValidateAccessToken(token, 15*time.Minute) {
				logger.LogWarn("Expired or invalid fundraiser access token from %s for formID %s", logger.GetClientIP(r), formID)
				showTokenExpiredPage(w, "fundraiser")
				return
			}

			// Check one-time use (only for incomplete payments)
			if sub.PayPalStatus != "COMPLETED" {
				if security.UseAccessToken(token) == nil {
					logger.LogWarn("Token already used for incomplete fundraiser payment from %s for formID %s", logger.GetClientIP(r), formID)
					showTokenExpiredPage(w, "fundraiser")
					return
				}
			} else {
				logger.LogInfo("Allowing access to completed fundraiser payment for %s", formID)
			}

			// Verify formID matches token (security check)
			if tokenInfo.FormID != formID {
				logger.LogWarn("FormID mismatch for fundraiser token from %s", logger.GetClientIP(r))
				http.Error(w, "Invalid access", http.StatusForbidden)
				return
			}
		}
	}

	// 3. Send emails if needed (keep your existing logic)
	if !isAdminView && sub.PayPalStatus == "COMPLETED" {
		if err := sendFundraiserConfirmationEmailIfNeeded(sub); err != nil {
			logger.LogError("Failed to send fundraiser confirmation email for %s: %v", formID, err)
		}
		if err := sendFundraiserAdminNotificationIfNeeded(sub); err != nil {
			logger.LogError("Failed to send fundraiser admin notification for %s: %v", formID, err)
		}
	}

	// 4. Prepare response for template
	resp := struct {
		FormID             string
		FullName           string
		FirstName          string
		LastName           string
		Email              string
		School             string
		Describe           string
		DonorStatus        string
		StudentCount       int
		Students           []data.Student
		DonationItems      []data.StudentDonation
		TotalAmount        float64
		CalculatedAmount   float64
		CoverFees          bool
		ProcessingFee      float64
		SubmittedAt        *time.Time
		PayPalOrderID      string
		PayPalStatus       string
		ConfirmationSent   bool
		ConfirmationSentAt *time.Time
		AdminNotified      bool
		AdminNotifiedAt    *time.Time
		IsCompleted        bool
		IsAdminView        bool
		Year               int
	}{
		FormID:             sub.FormID,
		FullName:           sub.FullName,
		FirstName:          sub.FirstName,
		LastName:           sub.LastName,
		Email:              sub.Email,
		School:             sub.School,
		Describe:           sub.Describe,
		DonorStatus:        sub.DonorStatus,
		StudentCount:       sub.StudentCount,
		Students:           sub.Students,
		DonationItems:      sub.DonationItems,
		TotalAmount:        sub.TotalAmount,
		CalculatedAmount:   sub.CalculatedAmount,
		CoverFees:          sub.CoverFees,
		ProcessingFee:      sub.CalculatedAmount - sub.TotalAmount,
		SubmittedAt:        sub.SubmittedAt,
		ConfirmationSent:   sub.ConfirmationEmailSent,
		ConfirmationSentAt: sub.ConfirmationEmailSentAt,
		AdminNotified:      sub.AdminNotificationSent,
		AdminNotifiedAt:    sub.AdminNotificationSentAt,
		PayPalOrderID:      sub.PayPalOrderID,
		PayPalStatus:       sub.PayPalStatus,
		IsCompleted:        sub.PayPalStatus == "COMPLETED",
		IsAdminView:        isAdminView,
		Year:               time.Now().Year(),
	}

	// 5. Render template (create a new one, or reuse fundraiserSummaryTmpl for now)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := fundraisersuccessTmpl.Execute(w, resp); err != nil {
		logger.LogError("Failed to render fundraiser success template: %v", err)
		http.Error(w, "Error rendering page", http.StatusInternalServerError)
	}
}

// emails and other notifications

func sendFundraiserConfirmationEmailIfNeeded(sub *data.FundraiserSubmission) error {
	if sub.ConfirmationEmailSent {
		logger.LogInfo("Fundraiser confirmation email already sent for form %s, skipping", sub.FormID)
		return nil
	}

	config := email.LoadEmailConfig()
	emaildata := email.FundraiserConfirmationData{
		FormID:           sub.FormID,
		FullName:         sub.FullName,
		FirstName:        sub.FirstName,
		Email:            sub.Email,
		School:           sub.School,
		Describe:         sub.Describe,
		DonorStatus:      sub.DonorStatus,
		Students:         sub.Students,
		DonationItems:    sub.DonationItems,
		TotalAmount:      sub.TotalAmount,
		CalculatedAmount: sub.CalculatedAmount,
		CoverFees:        sub.CoverFees,
		PayPalOrderID:    sub.PayPalOrderID,
		SubmittedAt:      sub.SubmittedAt,
		Year:             time.Now().Year(),
	}

	if err := email.SendFundraiserConfirmation(config, emaildata); err != nil {
		return err
	}

	// Mark as sent in the database
	if err := data.UpdateFundraiserEmailStatus(sub.FormID, true, sub.AdminNotificationSent); err != nil {
		logger.LogWarn("Failed to update fundraiser confirmation email status for %s: %v", sub.FormID, err)
	}
	return nil
}

func sendFundraiserAdminNotificationIfNeeded(sub *data.FundraiserSubmission) error {
	if sub.AdminNotificationSent {
		logger.LogInfo("Fundraiser admin notification already sent for form %s, skipping", sub.FormID)
		return nil
	}

	config := email.LoadEmailConfig()
	emaildata := email.FundraiserConfirmationData{
		FormID:           sub.FormID,
		FullName:         sub.FullName,
		FirstName:        sub.FirstName,
		Email:            sub.Email,
		School:           sub.School,
		Describe:         sub.Describe,
		DonorStatus:      sub.DonorStatus,
		Students:         sub.Students,
		DonationItems:    sub.DonationItems,
		TotalAmount:      sub.TotalAmount,
		CalculatedAmount: sub.CalculatedAmount,
		CoverFees:        sub.CoverFees,
		PayPalOrderID:    sub.PayPalOrderID,
		SubmittedAt:      sub.SubmittedAt,
		Year:             time.Now().Year(),
	}

	if err := email.SendFundraiserAdminNotification(config, emaildata); err != nil {
		return err
	}

	// Mark as sent in the database
	if err := data.UpdateFundraiserEmailStatus(sub.FormID, sub.ConfirmationEmailSent, true); err != nil {
		logger.LogWarn("Failed to update fundraiser admin notification status for %s: %v", sub.FormID, err)
	}
	return nil
}
