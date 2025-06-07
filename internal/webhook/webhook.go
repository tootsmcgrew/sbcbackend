package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"sbcbackend/internal/config"
	"sbcbackend/internal/data"
	"sbcbackend/internal/email"
	"sbcbackend/internal/logger"
	"sbcbackend/internal/payment"
)

// PayPalWebhookHandler processes incoming PayPal webhook POSTs.
func PayPalWebhookHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	logger.LogInfo("Received PayPal webhook request")
	logger.LogHTTPRequest(r)

	payloadBytes, err := io.ReadAll(r.Body)
	if err != nil {
		logger.LogHTTPError(r, http.StatusBadRequest, err)
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	transmissionID := r.Header.Get("Paypal-Transmission-Id")
	logger.LogInfo("Verifying webhook transmission ID: %s", transmissionID)

	if !verifyPayPalWebhookSignature(
		transmissionID,
		r.Header.Get("Paypal-Transmission-Sig"),
		r.Header.Get("Paypal-Transmission-Time"),
		r.Header.Get("Paypal-Cert-Url"),
		r.Header.Get("Paypal-Auth-Algo"),
		payloadBytes,
	) {
		logger.LogError("Invalid PayPal webhook signature")
		http.Error(w, "Invalid signature", http.StatusUnauthorized)
		return
	}

	// Parse incoming webhook event
	var event map[string]interface{}
	if err := json.Unmarshal(payloadBytes, &event); err != nil {
		logger.LogHTTPError(r, http.StatusBadRequest, err)
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	eventType, _ := event["event_type"].(string)
	logger.LogInfo("Webhook event type: %s", eventType)

	resource, _ := event["resource"].(map[string]interface{})
	if resource == nil {
		logger.LogInfo("No resource in event, ignoring")
		w.WriteHeader(http.StatusOK)
		return
	}

	formID := extractFormIDFromResource(resource)
	if formID == "" {
		logger.LogInfo("No form ID (invoice_id) found, ignoring webhook")
		w.WriteHeader(http.StatusOK)
		return
	}

	// --- DB-native reconciliation ---
	// Extract status (try "status" at top level, or in resource/capture_response)
	payPalStatus := ""
	if status, ok := resource["status"].(string); ok {
		payPalStatus = status
	} else if capture, ok := resource["capture_response"].(map[string]interface{}); ok {
		if s, ok := capture["status"].(string); ok {
			payPalStatus = s
		}
	} else {
		payPalStatus = eventType // fallback for rare cases
	}

	// Save the entire resource JSON for audit and reporting
	resourceJSON, err := json.Marshal(resource)
	if err != nil {
		logger.LogWarn("Failed to marshal resource JSON for formID %s: %v", formID, err)
		resourceJSON = []byte("{}")
	}

	if err := data.UpdateMembershipPayPalDetails(formID, payPalStatus, string(resourceJSON)); err != nil {
		logger.LogWarn("Failed to update PayPal webhook for %s: %v", formID, err)
	}

	// Optional: email alert for ops/monitoring
	subject := fmt.Sprintf("PayPal Webhook: %s", eventType)
	body := fmt.Sprintf("Received PayPal webhook for formID %s:\n\n%s%s", formID, string(payloadBytes), config.WebhookMockNotice())
	if err := email.SendAlertEmail(subject, body); err != nil {
		logger.LogWarn("Failed to send email alert: %v", err)
	}

	logger.LogInfo("Webhook for form %s processed successfully.", formID)
	w.WriteHeader(http.StatusOK)
}

// verifyPayPalWebhookSignature verifies the authenticity of the webhook.
func verifyPayPalWebhookSignature(
	transmissionID, transmissionSig, transmissionTime, certURL, authAlgo string,
	payload []byte,
) bool {
	if config.UseMockWebhookVerification {
		logger.LogInfo("Mock webhook verification enabled. Skipping real verification.")
		return true
	}

	ctx := context.Background()

	if config.PayPalWebhookID == "" {
		logger.LogWarn("Missing PAYPAL_WEBHOOK_ID; signature verification will fail")
		return false
	}

	accessToken, err := payment.GetPayPalAccessToken(ctx)
	if err != nil {
		logger.LogError("Failed to get access token for webhook verification: %v", err)
		return false
	}

	verificationPayload := map[string]interface{}{
		"auth_algo":         authAlgo,
		"cert_url":          certURL,
		"transmission_id":   transmissionID,
		"transmission_sig":  transmissionSig,
		"transmission_time": transmissionTime,
		"webhook_id":        config.PayPalWebhookID,
		"webhook_event":     json.RawMessage(payload),
	}

	bodyBytes, err := json.Marshal(verificationPayload)
	if err != nil {
		logger.LogError("Failed to marshal verification payload: %v", err)
		return false
	}

	req, err := http.NewRequest("POST", fmt.Sprintf("%s/v1/notifications/verify-webhook-signature", config.APIBase()), strings.NewReader(string(bodyBytes)))
	if err != nil {
		logger.LogError("Failed to create webhook verification request: %v", err)
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.LogError("Webhook verification request failed: %v", err)
		return false
	}
	defer resp.Body.Close()

	var result struct {
		VerificationStatus string `json:"verification_status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		logger.LogError("Failed to decode verification response: %v", err)
		return false
	}

	logger.LogInfo("Webhook verification status: %s", result.VerificationStatus)
	return result.VerificationStatus == "SUCCESS"
}

// extractFormIDFromResource tries to find the invoice_id (formID).
func extractFormIDFromResource(resource map[string]interface{}) string {
	if purchaseUnits, ok := resource["purchase_units"].([]interface{}); ok && len(purchaseUnits) > 0 {
		if unit, ok := purchaseUnits[0].(map[string]interface{}); ok {
			if formID, ok := unit["invoice_id"].(string); ok {
				return formID
			}
		}
	}
	if invoiceID, ok := resource["invoice_id"].(string); ok {
		return invoiceID
	}
	return ""
}
