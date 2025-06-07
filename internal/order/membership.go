// internal/order/membership.go
package order

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"sbcbackend/internal/data"
	"sbcbackend/internal/email"
	"sbcbackend/internal/logger"
	"sbcbackend/internal/security"
)

// types

// MembershipItemDisplay represents a formatted membership item for template display
type MembershipItemDisplay struct {
	ItemName   string
	ItemLabel  string
	Quantity   int
	UnitPrice  float64
	TotalPrice float64
	IsAddOn    bool
	IsFee      bool
	IsDonation bool
}

// checkout flow code goes below, from front to back

// parsing, processing

// handleMembershipOrderDetails processes membership order details
func handleMembershipOrderDetails(w http.ResponseWriter, r *http.Request, formID, token string) {
	sub, err := data.GetMembershipByID(formID)
	if err != nil {
		logger.LogError("GetMembershipByID failed for %s: %v", formID, err)
		http.Error(w, "Payment details not found", http.StatusNotFound)
		return
	}

	// Validate access token matches
	if sub.AccessToken != token {
		logger.LogWarn("Access token mismatch for formID %s from %s", formID, logger.GetClientIP(r))
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Always provide arrays, not nulls
	addons := sub.Addons
	if addons == nil {
		addons = []string{}
	}

	// Create membership items display similar to event items
	membershipItemsDisplay, totalFromSelections := formatMembershipItemsForDisplay(sub)

	// Compose the struct for template (matching event structure)
	resp := struct {
		FormID       string
		FormType     string
		FullName     string
		FirstName    string
		LastName     string
		Email        string
		School       string
		StudentCount int
		Students     []data.Student

		// Membership-specific fields
		Membership       string
		MembershipStatus string
		Describe         string
		Addons           []string
		Fees             map[string]int
		Donation         float64

		// Formatted display items (similar to EventItemsDisplay)
		MembershipItemsDisplay []MembershipItemDisplay

		// Financial fields
		CalculatedAmount    float64
		CoverFees           bool
		ProcessingFee       float64
		SubmittedAt         *time.Time
		TotalFromSelections float64
	}{
		FormID:                 sub.FormID,
		FormType:               "membership",
		FullName:               sub.FullName,
		FirstName:              sub.FirstName,
		LastName:               sub.LastName,
		Email:                  sub.Email,
		School:                 formatDisplayName(sub.School),
		StudentCount:           sub.StudentCount,
		Students:               sub.Students,
		Membership:             sub.Membership,
		MembershipStatus:       sub.MembershipStatus,
		Describe:               formatDisplayName(sub.Describe),
		Addons:                 addons,
		Fees:                   sub.Fees,
		Donation:               sub.Donation,
		MembershipItemsDisplay: membershipItemsDisplay,
		CalculatedAmount:       sub.CalculatedAmount,
		CoverFees:              sub.CoverFees,
		ProcessingFee:          calculateProcessingFee(sub.CalculatedAmount, sub.CoverFees),
		SubmittedAt:            sub.SubmittedAt,
		TotalFromSelections:    totalFromSelections,
	}

	logger.LogInfo("Membership order details accessed for form %s", formID)

	// Render template or return JSON based on Accept header
	acceptHeader := r.Header.Get("Accept")
	if strings.Contains(acceptHeader, "text/html") || strings.HasSuffix(r.URL.Path, ".html") {
		// Use the event order summary template (unified template)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := orderSummaryTmpl.Execute(w, resp); err != nil {
			logger.LogError("Failed to render membership order summary template: %v", err)
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

func handleMembershipSuccessPage(w http.ResponseWriter, r *http.Request, formID, token string, isAdminView bool, adminToken string) {
	// Check for admin token access
	var tokenInfo *security.TokenInfo

	if isAdminView {
		referer := r.Header.Get("Referer")
		if !security.ValidateAdminToken(adminToken, true, referer) {
			logger.LogWarn("Invalid admin token access attempt to success page from %s (referer: %s)", logger.GetClientIP(r), referer)
			http.Error(w, "Invalid admin access", http.StatusForbidden)
			return
		}
		logger.LogInfo("Valid admin token access to success page for formID %s from %s", formID, logger.GetClientIP(r))
		// Skip normal token validation for admin access
		goto loadSuccessData
	}

	// Normal user validation
	if token == "" {
		logger.LogWarn("Success page accessed without token from %s", logger.GetClientIP(r))
		showTokenExpiredPage(w, "membership")
		return
	}

loadSuccessData:
	sub, err := data.GetMembershipByID(formID)
	if err != nil {
		logger.LogError("GetMembershipByID failed for %s: %v", formID, err)
		http.Error(w, "Order details not found", http.StatusNotFound)
		return
	}

	// Token validation for non-admin users (after loading sub)
	if !isAdminView {
		// Add token expiration validation before database lookup
		tokenInfo = security.GetTokenInfo(token)

		if tokenInfo == nil {
			// Fallback: For completed payments, check against database token
			if sub.PayPalStatus == "COMPLETED" && sub.AccessToken == token {
				logger.LogInfo("Using database token validation for completed membership payment %s (server restart recovery)", formID)
			} else {
				logger.LogWarn("Token not found in memory and payment not completed for membership %s from %s", formID, logger.GetClientIP(r))
				showTokenExpiredPage(w, "membership")
				return
			}
		} else {
			// Token found in memory - use normal validation flow
			if !security.ValidateAccessToken(token, 15*time.Minute) {
				logger.LogWarn("Expired or invalid access token from %s for formID %s", logger.GetClientIP(r), formID)
				showTokenExpiredPage(w, "membership")
				return
			}

			// Check one-time use (only for incomplete payments)
			if sub.PayPalStatus != "COMPLETED" {
				if security.UseAccessToken(token) == nil {
					logger.LogWarn("Token already used or invalid from %s for formID %s", logger.GetClientIP(r), formID)
					showTokenExpiredPage(w, "membership")
					return
				}
			} else {
				logger.LogInfo("Allowing access to completed membership payment for %s", formID)
			}

			// Verify formID matches token (security check)
			if tokenInfo.FormID != formID {
				logger.LogWarn("FormID mismatch for token from %s", logger.GetClientIP(r))
				http.Error(w, "Invalid access", http.StatusForbidden)
				return
			}
		}
	}

	// Send confirmation email only for normal user access (not admin views)
	if !isAdminView && sub.PayPalStatus == "COMPLETED" {
		if err := sendConfirmationEmailIfNeeded(sub); err != nil {
			// Log error but don't fail the request - user should still see success page
			logger.LogError("Failed to send confirmation email for %s: %v", formID, err)
		}

		// Also send admin notification
		if err := sendAdminNotificationIfNeeded(sub); err != nil {
			logger.LogError("Failed to send admin notification for %s: %v", formID, err)
		}
	} else if isAdminView {
		logger.LogInfo("Skipping email sending for admin view of formID %s", formID)
	}

	// Extract enhanced PayPal fee info
	paypalFee := extractPayPalFee(sub.PayPalDetails)

	// Prepare enhanced data for template
	addons := sub.Addons
	if addons == nil {
		addons = []string{}
	}

	resp := struct {
		// Order identification
		FormID      string
		FormattedID string

		// Customer info
		FullName         string
		FirstName        string
		Email            string
		School           string
		Membership       string
		MembershipStatus string
		Describe         string

		// Students
		Students     []data.Student
		StudentCount int
		StudentList  string

		// What they bought
		Addons           []string
		AddonsList       string
		Fees             map[string]int
		FeesList         string
		Donation         float64
		CalculatedAmount float64
		CoverFees        bool

		// Payment info
		PayPalOrderID string
		PayPalStatus  string
		PayPalFee     float64
		NetAmount     float64

		// Timestamps (actual data)
		SubmissionDate time.Time
		OrderCreatedAt *time.Time
		SubmittedAt    *time.Time
		ProcessingTime string

		// Email status (actual data)
		ConfirmationSent   bool
		ConfirmationSentAt *time.Time
		AdminNotified      bool
		AdminNotifiedAt    *time.Time

		// Status and admin info
		IsCompleted bool
		IsAdminView bool
		Year        int
	}{
		FormID:             sub.FormID,
		FormattedID:        formatReceiptID(sub.FormID),
		FullName:           formatDisplayName(sub.FullName),
		FirstName:          formatDisplayName(sub.FirstName),
		Email:              sub.Email,
		School:             formatDisplayName(sub.School),
		Membership:         formatDisplayName(sub.Membership),
		MembershipStatus:   formatDisplayName(sub.MembershipStatus),
		Describe:           formatDisplayName(sub.Describe),
		Students:           sub.Students,
		StudentCount:       sub.StudentCount,
		StudentList:        formatStudentList(sub.Students),
		Addons:             addons,
		AddonsList:         formatList(addons),
		Fees:               sub.Fees,
		FeesList:           formatFeesMap(sub.Fees),
		Donation:           float64(sub.Donation),
		CalculatedAmount:   sub.CalculatedAmount,
		CoverFees:          sub.CoverFees,
		PayPalOrderID:      sub.PayPalOrderID,
		PayPalStatus:       sub.PayPalStatus,
		PayPalFee:          float64(paypalFee),
		NetAmount:          sub.CalculatedAmount - paypalFee,
		SubmissionDate:     sub.SubmissionDate,
		OrderCreatedAt:     sub.PayPalOrderCreatedAt,
		SubmittedAt:        sub.SubmittedAt,
		ProcessingTime:     calculateActualProcessingTime(sub),
		ConfirmationSent:   sub.ConfirmationEmailSent,
		ConfirmationSentAt: sub.ConfirmationEmailSentAt,
		AdminNotified:      sub.AdminNotificationSent,
		AdminNotifiedAt:    sub.AdminNotificationSentAt,
		IsCompleted:        sub.PayPalStatus == "COMPLETED",
		IsAdminView:        isAdminView,
		Year:               time.Now().Year(),
	}

	// Log successful access
	if isAdminView {
		logger.LogInfo("Admin success page accessed for form %s", formID)
	} else if tokenInfo != nil {
		logger.LogInfo("User success page accessed for form %s (token age: %v)", formID, time.Since(tokenInfo.CreatedAt))
	} else {
		logger.LogInfo("Success page accessed for form %s", formID)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := successPageTmpl.Execute(w, resp); err != nil {
		logger.LogError("Failed to render success template: %v", err)
		http.Error(w, "Error rendering page", http.StatusInternalServerError)
	}
}

// formatMembershipItemsForDisplay converts membership selections into display items
func formatMembershipItemsForDisplay(sub *data.MembershipSubmission) ([]MembershipItemDisplay, float64) {
	var itemsDisplay []MembershipItemDisplay
	var total float64

	// Use the global inventory service instead of loading files
	if inventoryService == nil {
		logger.LogWarn("Global inventory service not available for display formatting")
		return itemsDisplay, total
	}

	// 1. Add membership
	if sub.Membership != "" {
		if price, exists := inventoryService.GetMembershipPrice(sub.Membership); exists {
			itemsDisplay = append(itemsDisplay, MembershipItemDisplay{
				ItemName:   sub.Membership,
				ItemLabel:  sub.Membership,
				Quantity:   1,
				UnitPrice:  price,
				TotalPrice: price,
				IsAddOn:    false,
				IsFee:      false,
				IsDonation: false,
			})
			total += price
		}
	}

	// 2. Add fees (with quantities)
	for feeName, quantity := range sub.Fees {
		if quantity > 0 {
			if unitPrice, exists := inventoryService.GetFeePrice(feeName); exists {
				totalPrice := unitPrice * float64(quantity)

				itemsDisplay = append(itemsDisplay, MembershipItemDisplay{
					ItemName:   feeName,
					ItemLabel:  feeName,
					Quantity:   quantity,
					UnitPrice:  unitPrice,
					TotalPrice: totalPrice,
					IsAddOn:    false,
					IsFee:      true,
					IsDonation: false,
				})
				total += totalPrice
			}
		}
	}

	// 3. Add add-ons
	for _, addon := range sub.Addons {
		if addon != "" {
			if price, exists := inventoryService.GetProductPrice(addon); exists {
				itemsDisplay = append(itemsDisplay, MembershipItemDisplay{
					ItemName:   addon,
					ItemLabel:  addon,
					Quantity:   1,
					UnitPrice:  price,
					TotalPrice: price,
					IsAddOn:    true,
					IsFee:      false,
					IsDonation: false,
				})
				total += price
			}
		}
	}

	// 4. Add donation if present
	if sub.Donation > 0 {
		itemsDisplay = append(itemsDisplay, MembershipItemDisplay{
			ItemName:   "donation",
			ItemLabel:  "Extra Donation",
			Quantity:   1,
			UnitPrice:  sub.Donation,
			TotalPrice: sub.Donation,
			IsAddOn:    false,
			IsFee:      false,
			IsDonation: true,
		})
		total += sub.Donation
	}

	return itemsDisplay, total
}

// emails and other notifications

func sendConfirmationEmailIfNeeded(sub *data.MembershipSubmission) error {
	// Skip if already sent
	if sub.ConfirmationEmailSent {
		logger.LogInfo("Confirmation email already sent for form %s, skipping", sub.FormID)
		return nil
	}

	config := email.LoadEmailConfig()

	// Create email data
	emailData := email.MembershipConfirmationData{
		FormID:           sub.FormID,
		FullName:         sub.FullName,
		FirstName:        sub.FirstName,
		Email:            sub.Email,
		School:           sub.School,
		Membership:       sub.Membership,
		Students:         sub.Students,
		Addons:           sub.Addons,
		Fees:             sub.Fees,
		Donation:         sub.Donation,
		CalculatedAmount: sub.CalculatedAmount,
		CoverFees:        sub.CoverFees,
		PayPalOrderID:    sub.PayPalOrderID,
		SubmittedAt:      sub.SubmittedAt,
		Year:             time.Now().Year(),
	}

	// Send the email
	if err := email.SendMembershipConfirmation(config, emailData); err != nil {
		return fmt.Errorf("failed to send confirmation email: %w", err)
	}

	// Update database to mark email as sent
	if err := data.UpdateMembershipEmailStatus(sub.FormID, true, sub.AdminNotificationSent); err != nil {
		logger.LogError("Failed to update confirmation email status in database for %s: %v", sub.FormID, err)
		// Don't return error here - email was sent successfully
	}

	return nil
}

// sendAdminNotificationIfNeeded sends an admin notification for new submissions
func sendAdminNotificationIfNeeded(sub *data.MembershipSubmission) error {
	// Skip if already sent
	if sub.AdminNotificationSent {
		logger.LogInfo("Admin notification already sent for form %s, skipping", sub.FormID)
		return nil
	}

	config := email.LoadEmailConfig()

	emailData := email.MembershipConfirmationData{
		FormID:           sub.FormID,
		FullName:         sub.FullName,
		FirstName:        sub.FirstName,
		Email:            sub.Email,
		School:           sub.School,
		Membership:       sub.Membership,
		Students:         sub.Students,
		Addons:           sub.Addons,
		Fees:             sub.Fees,
		Donation:         sub.Donation,
		CalculatedAmount: sub.CalculatedAmount,
		CoverFees:        sub.CoverFees,
		PayPalOrderID:    sub.PayPalOrderID,
		SubmittedAt:      sub.SubmittedAt,
		Year:             time.Now().Year(),
	}

	// Send the notification
	if err := email.SendAdminNotification(config, emailData); err != nil {
		return fmt.Errorf("failed to send admin notification: %w", err)
	}

	// Update database to mark notification as sent
	if err := data.UpdateMembershipEmailStatus(sub.FormID, sub.ConfirmationEmailSent, true); err != nil {
		logger.LogError("Failed to update admin notification status in database for %s: %v", sub.FormID, err)
		// Don't return error here - email was sent successfully
	}

	return nil
}
