// internal/payment/recovery.go
package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"sbcbackend/internal/config"
	"sbcbackend/internal/data"
	"sbcbackend/internal/logger"
)

// PayPalRecoveryService handles stuck/failed PayPal operations
type PayPalRecoveryService struct {
	maxRetries    int
	retryInterval time.Duration
}

func NewPayPalRecoveryService() *PayPalRecoveryService {
	return &PayPalRecoveryService{
		maxRetries:    3,
		retryInterval: time.Second * 2,
	}
}

// RecoverPayPalOrder attempts to recover a stuck PayPal operation
func (s *PayPalRecoveryService) RecoverPayPalOrder(ctx context.Context, formID, orderID string) error {
	logger.LogInfo("Attempting PayPal recovery for formID=%s, orderID=%s", formID, orderID)

	// Get fresh PayPal access token
	accessToken, err := GetPayPalAccessToken(ctx)
	if err != nil {
		return fmt.Errorf("failed to get PayPal access token during recovery: %w", err)
	}

	// Check current order status with PayPal
	orderDetails, err := s.getOrderDetailsWithRetry(ctx, orderID, accessToken)
	if err != nil {
		return fmt.Errorf("failed to get order details during recovery: %w", err)
	}

	status, ok := orderDetails["status"].(string)
	if !ok {
		return fmt.Errorf("invalid order status in PayPal response")
	}

	logger.LogInfo("PayPal order %s current status: %s", orderID, status)

	// Handle different order states
	switch status {
	case "COMPLETED":
		return s.syncCompletedOrder(formID, orderDetails)
	case "APPROVED":
		return s.attemptCapture(ctx, formID, orderID, accessToken)
	case "CREATED", "SAVED":
		logger.LogInfo("Order %s is still pending customer approval", orderID)
		return nil // Nothing to recover, customer hasn't approved yet
	case "CANCELLED", "EXPIRED":
		return s.handleFailedOrder(formID, status)
	default:
		logger.LogWarn("Unknown PayPal order status for %s: %s", orderID, status)
		return nil
	}
}

func (s *PayPalRecoveryService) getOrderDetailsWithRetry(ctx context.Context, orderID, accessToken string) (map[string]interface{}, error) {
	var lastErr error

	for attempt := 1; attempt <= s.maxRetries; attempt++ {
		orderDetails, err := GetPayPalOrderDetails(orderID, accessToken)
		if err == nil {
			return orderDetails, nil
		}

		lastErr = err
		logger.LogWarn("PayPal order details attempt %d failed: %v", attempt, err)

		if attempt < s.maxRetries {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(s.retryInterval * time.Duration(attempt)):
				// Exponential backoff
			}
		}
	}

	return nil, fmt.Errorf("failed to get order details after %d attempts: %w", s.maxRetries, lastErr)
}

func (s *PayPalRecoveryService) syncCompletedOrder(formID string, orderDetails map[string]interface{}) error {
	logger.LogInfo("Syncing already completed PayPal order for formID=%s", formID)

	// Convert order details to JSON for storage
	detailsJSON, err := json.Marshal(orderDetails)
	if err != nil {
		return fmt.Errorf("failed to marshal order details: %w", err)
	}

	now := time.Now()
	formType := getFormTypeFromID(formID)

	// Update the appropriate form type
	switch formType {
	case "membership":
		return data.UpdateMembershipPayPalCapture(formID, string(detailsJSON), "COMPLETED", &now)
	case "fundraiser":
		return data.UpdateFundraiserPayPalCapture(formID, string(detailsJSON), "COMPLETED", &now)
	case "event":
		return data.UpdateEventPayPalCapture(formID, string(detailsJSON), "COMPLETED", &now)
	default:
		return fmt.Errorf("unknown form type: %s", formType)
	}
}

func (s *PayPalRecoveryService) attemptCapture(ctx context.Context, formID, orderID, accessToken string) error {
	logger.LogInfo("Attempting to capture approved PayPal order %s for formID=%s", orderID, formID)

	captureURL := fmt.Sprintf("%s/v2/checkout/orders/%s/capture", config.APIBase(), orderID)

	for attempt := 1; attempt <= s.maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", captureURL, strings.NewReader("{}"))
		if err != nil {
			return fmt.Errorf("failed to create capture request: %w", err)
		}

		req.Header.Set("Authorization", accessToken)
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{Timeout: time.Second * 30}
		resp, err := client.Do(req)
		if err != nil {
			logger.LogWarn("PayPal capture attempt %d failed: %v", attempt, err)
			if attempt < s.maxRetries {
				time.Sleep(s.retryInterval * time.Duration(attempt))
				continue
			}
			return fmt.Errorf("failed to capture after %d attempts: %w", s.maxRetries, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusCreated {
			logger.LogInfo("Successfully captured PayPal order %s on attempt %d", orderID, attempt)
			return s.syncCompletedOrder(formID, map[string]interface{}{
				"id":               orderID,
				"status":           "COMPLETED",
				"recovered":        true,
				"recovery_attempt": attempt,
			})
		}

		logger.LogWarn("PayPal capture attempt %d returned status %d", attempt, resp.StatusCode)
		if attempt < s.maxRetries {
			time.Sleep(s.retryInterval * time.Duration(attempt))
		}
	}

	return fmt.Errorf("failed to capture PayPal order after %d attempts", s.maxRetries)
}

func (s *PayPalRecoveryService) handleFailedOrder(formID, status string) error {
	logger.LogWarn("PayPal order for formID=%s failed with status=%s", formID, status)

	// Mark the order as failed in the database so we can create a new one
	formType := getFormTypeFromID(formID)

	switch formType {
	case "membership":
		return data.UpdateMembershipPayPalDetails(formID, fmt.Sprintf("FAILED_%s", status), "")
	case "fundraiser":
		// Add similar functions for fundraiser if needed
		logger.LogWarn("Fundraiser order recovery not fully implemented for formID=%s", formID)
	case "event":
		// Add similar functions for event if needed
		logger.LogWarn("Event order recovery not fully implemented for formID=%s", formID)
	}

	return nil
}
