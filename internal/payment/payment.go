// internal/payment/payment.go
package payment

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"sbcbackend/internal/config"
	"sbcbackend/internal/data"
	"sbcbackend/internal/food"
	"sbcbackend/internal/inventory"
	"sbcbackend/internal/logger"
	"sbcbackend/internal/middleware"
)

const (
	paypalAPIBaseSandbox = "https://api.sandbox.paypal.com" // Not used, in config
	paypalAPIBaseLive    = "https://api.paypal.com"         // Not used, in config
)

const (
	unmatchedPaymentsDir = "/home/protected/boosterbackend/data/unmatched-payments"
)

var timeZone *time.Location

var (
	cachedPayPalToken     string
	cachedPayPalExpiresAt time.Time
	tokenMu               sync.Mutex
)

// inject Inventory service from Main
var (
	inventoryService *inventory.Service
	recoveryService  *PayPalRecoveryService
)

// initialize the service:
func SetInventoryService(service *inventory.Service) {
	inventoryService = service
}

type PaymentDetails struct {
	Amount     float64 `json:"calculated_amount"`
	Membership string  `json:"membership"`
	// Add more fields if needed
}

// CreateOrderRequest represents the standardized request for creating orders
type CreateOrderRequest struct {
	FormID string `json:"formID" validate:"required"`
}

// CreateOrderResponse represents the standardized response for creating orders
type CreateOrderResponse struct {
	OrderID string `json:"orderID"`
	FormID  string `json:"formID"`
}

type SavePaymentInput struct {
	FormID     string         `json:"formID"`
	Amount     float64        `json:"amount,omitempty"`
	Membership string         `json:"membership"`
	Addons     []string       `json:"addons"`
	Fees       map[string]int `json:"fees"` // Changed to map for quantity
	Donation   float64        `json:"donation"`
	CoverFees  bool           `json:"cover_fees"`
}

type PayPalTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	AppID       string `json:"app_id"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope,omitempty"`
}

// PayPalError represents an error response from the PayPal API
type PayPalError struct {
	Name        string `json:"name"`
	Message     string `json:"message"`
	DebugID     string `json:"debug_id"`
	Information []struct {
		Location string `json:"location"`
		Issue    string `json:"issue"`
	} `json:"information,omitempty"`
}

func init() {
	var err error
	timeZone, err = time.LoadLocation("America/Chicago")
	if err != nil {
		log.Fatalf("Error loading time zone: %v", err)
	}
	recoveryService = NewPayPalRecoveryService()
}

func getIntField(data map[string]interface{}, key string) int {
	if v, ok := data[key]; ok {
		switch val := v.(type) {
		case float64:
			return int(val)
		case int:
			return val
		case string:
			i, _ := strconv.Atoi(val)
			return i
		}
	}
	return 0
}

func GetPayPalAccessToken(ctx context.Context) (string, error) {
	// Check cache first
	tokenMu.Lock()
	if cachedPayPalToken != "" && time.Now().Before(cachedPayPalExpiresAt) {
		token := cachedPayPalToken
		tokenMu.Unlock()
		logger.LogInfo("Using cached PayPal access token (expires at %v)", cachedPayPalExpiresAt)
		return token, nil
	}
	tokenMu.Unlock()

	// Not cached or expired; fetch new token
	authURL := fmt.Sprintf("%s/v1/oauth2/token", config.APIBase())
	formData := url.Values{}
	formData.Set("grant_type", "client_credentials")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return "", fmt.Errorf("creating PayPal auth request: %w", err)
	}
	req.SetBasicAuth(config.ClientID(), config.ClientSecret())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
			MaxIdleConns:        10,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  false,
			MaxIdleConnsPerHost: 5,
		},
	}

	logger.LogInfo("Requesting new PayPal access token")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("executing PayPal auth request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading PayPal response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		logger.LogError("PayPal API error (HTTP %d): %s", resp.StatusCode, string(body))
		return "", fmt.Errorf("PayPal API returned status %d: %s", resp.StatusCode, string(body))
	}

	var result PayPalTokenResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parsing PayPal auth response: %w", err)
	}

	if result.AccessToken == "" {
		return "", fmt.Errorf("access token not found in PayPal response")
	}

	// Cache the token and its expiry time (renew 1 minute before actual expiry)
	tokenMu.Lock()
	cachedPayPalToken = fmt.Sprintf("%s %s", result.TokenType, result.AccessToken)
	cachedPayPalExpiresAt = time.Now().Add(time.Duration(result.ExpiresIn-60) * time.Second)
	token := cachedPayPalToken
	tokenMu.Unlock()

	logger.LogInfo("Fetched and cached new PayPal access token (expires at %v)", cachedPayPalExpiresAt)
	return token, nil
}

// GetPayPalOrderDetails fetches order details using the order ID.
func GetPayPalOrderDetails(orderID, accessToken string) (map[string]interface{}, error) {
	url := fmt.Sprintf("%s/v2/checkout/orders/%s", config.APIBase(), orderID) // Use config
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		logger.LogError("Failed to create PayPal order details request: %v", err)
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	logger.LogInfo("Fetching PayPal order details for order %s", orderID)
	client := &http.
		Client{}
	resp, err := client.Do(req)
	if err != nil {
		logger.LogError("Failed to execute PayPal order details request: %v", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		err := fmt.Errorf("failed to fetch order details: %s", string(body))
		logger.LogError("PayPal API error for order %s: %v (HTTP %d)", orderID, err, resp.StatusCode)
		return nil, err
	}

	var orderDetails map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&orderDetails)
	if err != nil {
		logger.LogError("Failed to decode PayPal order details for order %s: %v", orderID, err)
		return nil, err
	}

	logger.LogInfo("Successfully retrieved PayPal order details for order %s", orderID)
	return orderDetails, nil
}

// CreatePayPalOrder creates a new PayPal order with given purchase details using the API.
func CreatePayPalOrder(accessToken string, orderData map[string]interface{}) (map[string]interface{}, error) {
	url := fmt.Sprintf("%s/v2/checkout/orders", config.APIBase())

	bodyBytes, err := json.Marshal(orderData)
	if err != nil {
		logger.LogError("Failed to marshal order data: %v", err)
		return nil, err
	}

	req, err := http.NewRequest("POST", url, strings.NewReader(string(bodyBytes)))
	if err != nil {
		logger.LogError("Failed to create PayPal order creation request: %v", err)
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", accessToken)

	logger.LogInfo("Creating PayPal order")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		logger.LogError("Failed to execute PayPal order creation request: %v", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		err := fmt.Errorf("failed to create order: %s", string(body))
		logger.LogError("PayPal API error: %v (HTTP %d)", err, resp.StatusCode)
		return nil, err
	}

	var orderResponse map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&orderResponse); err != nil {
		logger.LogError("Failed to decode PayPal order creation response: %v", err)
		return nil, err
	}

	logger.LogInfo("Successfully created PayPal order")
	return orderResponse, nil
}

// CreatePayPalOrderHandler reads formID from query, builds order, and creates PayPal order
type OrderRequest struct {
	FormID string `json:"formID"`
	Token  string `json:"token"`
}

// CreatePayPalOrderHandlerV2 is the new unified version
func CreatePayPalOrderHandler(w http.ResponseWriter, r *http.Request) {
	var req CreateOrderRequest
	if err := middleware.ParseJSONRequest(r, &req); err != nil {
		middleware.WriteAPIError(w, r, http.StatusBadRequest, "invalid_request",
			"Invalid JSON request", err.Error())
		return
	}

	token := middleware.GetToken(r.Context())

	// Validate access to form
	if err := middleware.ValidateFormIDAccess(r.Context(), req.FormID, token); err != nil {
		middleware.WriteAPIError(w, r, http.StatusForbidden, "access_denied",
			"Access denied to this form", "")
		return
	}

	// Use existing form type detection
	formType := getFormTypeFromID(req.FormID)

	var calculatedAmount float64
	var description string
	var existingOrderID string

	// Load data using existing functions
	switch formType {
	case "membership":
		sub, err := data.GetMembershipByID(req.FormID)
		if err != nil {
			logger.LogError("Membership not found for formID %s: %v", req.FormID, err)
			http.Error(w, "Order not found", http.StatusNotFound)
			return
		}
		if sub.AccessToken != token {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		calculatedAmount = sub.CalculatedAmount
		description = sub.Membership
		existingOrderID = sub.PayPalOrderID

	case "fundraiser":
		sub, err := data.GetFundraiserByID(req.FormID)
		if err != nil {
			logger.LogError("Fundraiser not found for formID %s: %v", req.FormID, err)
			http.Error(w, "Order not found", http.StatusNotFound)
			return
		}
		if sub.AccessToken != token {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		calculatedAmount = sub.CalculatedAmount
		description = fmt.Sprintf("Practice-a-Thon Donation (%d students)", len(sub.DonationItems))
		existingOrderID = sub.PayPalOrderID

	case "event":
		sub, err := data.GetEventByID(req.FormID)
		if err != nil {
			logger.LogError("Event not found for formID %s: %v", req.FormID, err)
			http.Error(w, "Order not found", http.StatusNotFound)
			return
		}
		if sub.AccessToken != token {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		calculatedAmount = sub.CalculatedAmount
		description = fmt.Sprintf("%s Registration", sub.Event)
		existingOrderID = sub.PayPalOrderID

	default:
		http.Error(w, "Unknown form type", http.StatusBadRequest)
		return
	}

	// NEW: Check if order already exists and attempt recovery if needed
	if existingOrderID != "" {
		logger.LogInfo("Existing PayPal order found for %s: %s", req.FormID, existingOrderID)

		// Attempt recovery to sync the order status
		if err := recoveryService.RecoverPayPalOrder(r.Context(), req.FormID, existingOrderID); err != nil {
			logger.LogWarn("PayPal recovery failed for %s: %v", req.FormID, err)
			// Continue with existing order - recovery failure shouldn't block user
		}

		response := CreateOrderResponse{
			OrderID: existingOrderID,
			FormID:  req.FormID,
		}
		middleware.WriteAPISuccess(w, r, response)
		return
	}

	// Validate amount
	if calculatedAmount <= 0 {
		logger.LogError("Attempt to create PayPal order with zero/negative amount for formID %s (%.2f)",
			req.FormID, calculatedAmount)
		http.Error(w, "Invalid order amount. Cannot create PayPal order.", http.StatusBadRequest)
		return
	}

	logger.LogInfo("Creating PayPal order for %s (%s): %.2f", req.FormID, formType, calculatedAmount)

	// Create PayPal order data
	orderData := map[string]interface{}{
		"intent": "CAPTURE",
		"purchase_units": []map[string]interface{}{
			{
				"amount": map[string]interface{}{
					"currency_code": "USD",
					"value":         fmt.Sprintf("%.2f", calculatedAmount),
				},
				"description": description,
				"invoice_id":  req.FormID,
			},
		},
	}

	// NEW: Get PayPal access token with retry
	accessToken, err := getPayPalAccessTokenWithRetry(r.Context(), 3)
	if err != nil {
		middleware.WriteAPIError(w, r, http.StatusInternalServerError, "paypal_error",
			"PayPal service unavailable", err.Error())
		return
	}

	// NEW: Create order with retry
	orderResponse, err := createPayPalOrderWithRetry(r.Context(), accessToken, orderData, 3)
	if err != nil {
		middleware.WriteAPIError(w, r, http.StatusInternalServerError, "order_creation_failed",
			"Failed to create PayPal order", err.Error())
		return
	}

	orderID, ok := orderResponse["id"].(string)
	if !ok || orderID == "" {
		middleware.WriteAPIError(w, r, http.StatusInternalServerError, "invalid_paypal_response",
			"Invalid PayPal response", "")
		return
	}

	// Update the appropriate form type with PayPal order ID using existing functions
	now := time.Now()
	switch formType {
	case "membership":
		if err := data.UpdateMembershipPayPalOrder(req.FormID, orderID, &now); err != nil {
			logger.LogError("Failed to update membership PayPal order: %v", err)
		}
	case "fundraiser":
		if err := data.UpdateFundraiserPayPalOrder(req.FormID, orderID, &now); err != nil {
			logger.LogError("Failed to update fundraiser PayPal order: %v", err)
		}
	case "event":
		if err := data.UpdateEventPayPalOrder(req.FormID, orderID, &now); err != nil {
			logger.LogError("Failed to update event PayPal order: %v", err)
		}
	}

	response := CreateOrderResponse{
		OrderID: orderID,
		FormID:  req.FormID,
	}

	middleware.WriteAPISuccess(w, r, response)
}

// CapturePayPalOrderHandler captures a PayPal order for any form type
func CapturePayPalOrderHandler(w http.ResponseWriter, r *http.Request) {
	logger.LogHTTPRequest(r)
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var input struct {
		OrderID string `json:"orderID"`
		FormID  string `json:"formID"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "Invalid input", http.StatusBadRequest)
		return
	}
	if input.OrderID == "" || input.FormID == "" {
		http.Error(w, "Missing orderID or formID", http.StatusBadRequest)
		return
	}

	accessToken := r.Header.Get("X-Access-Token")
	if accessToken == "" {
		accessToken = r.URL.Query().Get("token")
	}
	if accessToken == "" {
		http.Error(w, "Missing access token", http.StatusForbidden)
		return
	}

	// Use existing form type detection
	formType := getFormTypeFromID(input.FormID)

	// Validate access and check if already captured using existing functions
	switch formType {
	case "membership":
		sub, err := data.GetMembershipByID(input.FormID)
		if err != nil {
			http.Error(w, "Order not found", http.StatusNotFound)
			return
		}
		if sub.AccessToken != accessToken {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		// Idempotency check
		if sub.PayPalStatus == "COMPLETED" {
			json.NewEncoder(w).Encode(map[string]string{
				"status":  "COMPLETED",
				"message": "Order already processed",
			})
			return
		}

	case "fundraiser":
		sub, err := data.GetFundraiserByID(input.FormID)
		if err != nil {
			http.Error(w, "Order not found", http.StatusNotFound)
			return
		}
		if sub.AccessToken != accessToken {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		// Idempotency check
		if sub.PayPalStatus == "COMPLETED" {
			json.NewEncoder(w).Encode(map[string]string{
				"status":  "COMPLETED",
				"message": "Order already processed",
			})
			return
		}

	case "event":
		sub, err := data.GetEventByID(input.FormID)
		if err != nil {
			http.Error(w, "Order not found", http.StatusNotFound)
			return
		}
		if sub.AccessToken != accessToken {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		// Idempotency check
		if sub.PayPalStatus == "COMPLETED" {
			json.NewEncoder(w).Encode(map[string]string{
				"status":  "COMPLETED",
				"message": "Order already processed",
			})
			return
		}

	default:
		http.Error(w, "Unknown form type", http.StatusBadRequest)
		return
	}

	// NEW: First attempt recovery to see if the order was already captured
	logger.LogInfo("Attempting PayPal recovery before capture for formID=%s, orderID=%s", input.FormID, input.OrderID)
	if err := recoveryService.RecoverPayPalOrder(r.Context(), input.FormID, input.OrderID); err != nil {
		logger.LogWarn("PayPal recovery failed, proceeding with capture: %v", err)
	} else {
		// Recovery might have found the order was already captured
		// Check again if it's now completed
		switch formType {
		case "membership":
			if sub, err := data.GetMembershipByID(input.FormID); err == nil && sub.PayPalStatus == "COMPLETED" {
				json.NewEncoder(w).Encode(map[string]string{
					"status":  "COMPLETED",
					"message": "Order was already captured (recovered)",
				})
				return
			}
		case "fundraiser":
			if sub, err := data.GetFundraiserByID(input.FormID); err == nil && sub.PayPalStatus == "COMPLETED" {
				json.NewEncoder(w).Encode(map[string]string{
					"status":  "COMPLETED",
					"message": "Order was already captured (recovered)",
				})
				return
			}
		case "event":
			if sub, err := data.GetEventByID(input.FormID); err == nil && sub.PayPalStatus == "COMPLETED" {
				json.NewEncoder(w).Encode(map[string]string{
					"status":  "COMPLETED",
					"message": "Order was already captured (recovered)",
				})
				return
			}
		}
	}

	// Proceed with capture with retry logic
	ppToken, err := getPayPalAccessTokenWithRetry(r.Context(), 3)
	if err != nil {
		logger.LogError("PayPal access token error: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// NEW: Capture with retry
	captureResult, err := capturePayPalOrderWithRetry(r.Context(), input.OrderID, ppToken, 3)
	if err != nil {
		logger.LogError("PayPal capture failed for %s (%s): %v", input.FormID, formType, err)
		http.Error(w, "Payment capture failed", http.StatusInternalServerError)
		return
	}

	logger.LogInfo("PayPal order %s captured successfully for %s (%s)", input.OrderID, input.FormID, formType)

	// Update the appropriate form type with capture details using existing functions
	now := time.Now()
	switch formType {
	case "membership":
		if err := data.UpdateMembershipPayPalCapture(input.FormID, captureResult, "COMPLETED", &now); err != nil {
			logger.LogError("Failed to update membership PayPal capture: %v", err)
		}
	case "fundraiser":
		if err := data.UpdateFundraiserPayPalCapture(input.FormID, captureResult, "COMPLETED", &now); err != nil {
			logger.LogError("Failed to update fundraiser PayPal capture: %v", err)
		}
	case "event":
		if err := data.UpdateEventPayPalCapture(input.FormID, captureResult, "COMPLETED", &now); err != nil {
			logger.LogError("Failed to update event PayPal capture: %v", err)
		}
	}

	// Return the capture result to the frontend
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(captureResult))
}

func updatePayPalDetails(formData map[string]interface{}, eventType string, resource map[string]interface{}) {
	details, _ := formData["paypal_details"].(map[string]interface{})
	if details == nil {
		details = make(map[string]interface{})
	}

	details["last_event"] = eventType
	details["last_update"] = time.Now().Format(time.RFC3339)

	// Save useful fields from resource
	if id, _ := resource["id"].(string); id != "" {
		details["resource_id"] = id
	}
	if status, _ := resource["status"].(string); status != "" {
		details["status"] = status
	}
	if amountObj, ok := resource["amount"].(map[string]interface{}); ok {
		details["amount"] = amountObj
	}
	if payer, ok := resource["payer"].(map[string]interface{}); ok {
		details["payer"] = payer
	}
	if shipping, ok := resource["shipping"].(map[string]interface{}); ok {
		details["shipping"] = shipping
	}

	formData["paypal_details"] = details
}

// ProcessMembershipPayment processes and validates membership payment data using inventory service
func ProcessMembershipPayment(sub *data.MembershipSubmission, input SavePaymentInput) error {
	// Check if inventory service is available
	if inventoryService == nil {
		return fmt.Errorf("inventory service not initialized")
	}

	// Validate all selections using inventory service
	if err := inventoryService.ValidateAllSelections(input.Membership, input.Addons, input.Fees); err != nil {
		return fmt.Errorf("inventory validation failed: %w", err)
	}

	// Calculate total with tamper protection
	calculatedTotal, err := inventoryService.CalculateMembershipTotal(
		input.Membership, input.Addons, input.Fees, input.Donation, input.CoverFees,
	)
	if err != nil {
		return fmt.Errorf("total calculation failed: %w", err)
	}

	// Verify client-submitted total matches server calculation (tamper protection)
	if input.Amount > 0 && math.Abs(calculatedTotal-input.Amount) > 0.01 {
		return fmt.Errorf("total amount mismatch: client sent %.2f, server calculated %.2f",
			input.Amount, calculatedTotal)
	}

	// Update submission with validated data
	sub.Membership = input.Membership
	sub.Addons = input.Addons
	sub.Fees = input.Fees
	sub.Donation = input.Donation
	sub.CoverFees = input.CoverFees
	sub.CalculatedAmount = calculatedTotal

	// Save to database
	if err := data.UpdateMembershipPayment(*sub); err != nil {
		return fmt.Errorf("failed to update membership payment: %w", err)
	}

	logger.LogInfo("Membership payment processed for %s: Total=$%.2f", sub.FormID, calculatedTotal)
	return nil
}

// SaveEventPaymentHandler handles saving event payment selections
func SaveEventPaymentHandler(w http.ResponseWriter, r *http.Request) {
	logger.LogHTTPRequest(r)
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	accessToken := r.Header.Get("X-Access-Token")
	if accessToken == "" {
		accessToken = r.URL.Query().Get("token")
	}
	if accessToken == "" {
		http.Error(w, "Missing access token", http.StatusForbidden)
		return
	}

	var input struct {
		FormID       string `json:"formID"`
		EventOptions struct {
			StudentSelections map[string]map[string]bool `json:"student_selections"`
			SharedSelections  map[string]int             `json:"shared_selections"`
			CoverFees         bool                       `json:"cover_fees"`
			HasFoodOrders     bool                       `json:"has_food_orders"`
		} `json:"event_options"`
	}

	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if input.FormID == "" {
		http.Error(w, "Missing form ID", http.StatusBadRequest)
		return
	}

	// Load event submission
	sub, err := data.GetEventByID(input.FormID)
	if err != nil {
		http.Error(w, "Event not found", http.StatusNotFound)
		return
	}

	if sub.AccessToken != accessToken {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Don't allow changes to already paid events
	if sub.PayPalStatus == "COMPLETED" {
		http.Error(w, "This event has already been paid", http.StatusConflict)
		return
	}

	// Use inventory service for validation and calculation
	if inventoryService == nil {
		logger.LogError("Inventory service not available for event %s", input.FormID)
		http.Error(w, "Inventory service not available", http.StatusInternalServerError)
		return
	}

	// Validate event selections using inventory service
	if err := inventoryService.ValidateEventSelection(sub.Event, input.EventOptions.StudentSelections, input.EventOptions.SharedSelections); err != nil {
		logger.LogError("Event validation failed for %s: %v", input.FormID, err)
		http.Error(w, fmt.Sprintf("Invalid event selections: %v", err), http.StatusBadRequest)
		return
	}

	// Calculate total using inventory service
	total, err := inventoryService.CalculateEventTotal(sub.Event, input.EventOptions.StudentSelections, input.EventOptions.SharedSelections, input.EventOptions.CoverFees)
	if err != nil {
		logger.LogError("Event total calculation failed for %s: %v", input.FormID, err)
		http.Error(w, fmt.Sprintf("Calculation failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Store the selections as JSON in FoodChoicesJSON field
	selectionsJSON, err := json.Marshal(input.EventOptions)
	if err != nil {
		http.Error(w, "Failed to serialize selections", http.StatusInternalServerError)
		return
	}

	// Use the frontend-provided flag directly
	sub.HasFoodOrders = input.EventOptions.HasFoodOrders

	// Generate food order ID only if food was selected
	if sub.HasFoodOrders {
		foodOrderID, err := food.GenerateFoodOrderID(sub.School)
		if err != nil {
			logger.LogError("Failed to generate food order ID for %s: %v", input.FormID, err)
			sub.FoodOrderID = ""
		} else {
			sub.FoodOrderID = foodOrderID
			logger.LogInfo("Generated food order ID %s for %s (food selections: %v)", foodOrderID, input.FormID, sub.HasFoodOrders)
		}
	} else {
		sub.FoodOrderID = ""
		logger.LogInfo("No food selections for %s, no food order ID generated", input.FormID)
	}

	// Update the submission with calculated total
	sub.FoodChoicesJSON = string(selectionsJSON)
	sub.FoodChoices = map[string]string{
		"type":  "event_checkout_v2",
		"total": fmt.Sprintf("%.2f", total),
	}
	sub.CalculatedAmount = total
	sub.CoverFees = input.EventOptions.CoverFees

	// Save to database using existing update function
	if err := data.UpdateEventPayment(*sub); err != nil {
		logger.LogError("Failed to update event payment: %v", err)
		http.Error(w, "Failed to save payment data", http.StatusInternalServerError)
		return
	}

	logger.LogInfo("Event payment data saved for %s using inventory service: Total=$%.2f", input.FormID, total)

	// Return success
	json.NewEncoder(w).Encode(map[string]string{
		"formID":      input.FormID,
		"accessToken": accessToken,
		"status":      "success",
	})
}

// SaveMembershipPaymentHandler handles saving membership payment selections
func SaveMembershipPaymentHandler(w http.ResponseWriter, r *http.Request) {
	logger.LogHTTPRequest(r)
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	accessToken := r.Header.Get("X-Access-Token")
	if accessToken == "" {
		accessToken = r.URL.Query().Get("token")
	}
	if accessToken == "" {
		http.Error(w, "Missing access token", http.StatusForbidden)
		return
	}

	var input struct {
		FormID     string         `json:"formID"`
		Membership string         `json:"membership"`
		Addons     []string       `json:"addons"`
		Fees       map[string]int `json:"fees"`
		Donation   float64        `json:"donation"`
		CoverFees  bool           `json:"cover_fees"`
	}

	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if input.FormID == "" {
		http.Error(w, "Missing form ID", http.StatusBadRequest)
		return
	}

	// Load membership submission
	sub, err := data.GetMembershipByID(input.FormID)
	if err != nil {
		http.Error(w, "Membership not found", http.StatusNotFound)
		return
	}

	if sub.AccessToken != accessToken {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Don't allow changes to already paid memberships
	if sub.PayPalStatus == "COMPLETED" {
		http.Error(w, "This membership has already been paid", http.StatusConflict)
		return
	}

	// Check if inventory service is available
	if inventoryService == nil {
		http.Error(w, "Inventory service not available", http.StatusInternalServerError)
		return
	}

	// Validate all selections using inventory service
	if err := inventoryService.ValidateAllSelections(input.Membership, input.Addons, input.Fees); err != nil {
		logger.LogError("Membership validation failed for %s: %v", input.FormID, err)
		http.Error(w, fmt.Sprintf("Invalid selections: %v", err), http.StatusBadRequest)
		return
	}

	// Calculate total with tamper protection using inventory service
	calculatedTotal, err := inventoryService.CalculateMembershipTotal(
		input.Membership, input.Addons, input.Fees, input.Donation, input.CoverFees,
	)
	if err != nil {
		logger.LogError("Total calculation failed for %s: %v", input.FormID, err)
		http.Error(w, fmt.Sprintf("Calculation failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Update the submission with validated data
	sub.Membership = input.Membership
	sub.Addons = input.Addons
	sub.Fees = input.Fees
	sub.Donation = input.Donation
	sub.CoverFees = input.CoverFees
	sub.CalculatedAmount = calculatedTotal

	// Save to database using existing update function
	if err := data.UpdateMembershipPayment(*sub); err != nil {
		logger.LogError("Failed to update membership payment: %v", err)
		http.Error(w, "Failed to save payment data", http.StatusInternalServerError)
		return
	}

	logger.LogInfo("Membership payment data saved for %s: Total=$%.2f", input.FormID, calculatedTotal)

	// Return success (same format as event handler)
	json.NewEncoder(w).Encode(map[string]string{
		"formID":      input.FormID,
		"accessToken": accessToken,
		"status":      "success",
	})
}

// getFormTypeFromID extracts form type from formID prefix
func getFormTypeFromID(formID string) string {
	parts := strings.Split(formID, "-")
	if len(parts) > 0 {
		return parts[0]
	}
	return "unknown"
}

// recovery helpers

func getPayPalAccessTokenWithRetry(ctx context.Context, maxRetries int) (string, error) {
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		token, err := GetPayPalAccessToken(ctx)
		if err == nil {
			return token, nil
		}

		lastErr = err
		logger.LogWarn("PayPal access token attempt %d failed: %v", attempt, err)

		if attempt < maxRetries {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
				// Exponential backoff
			}
		}
	}

	return "", fmt.Errorf("failed to get PayPal access token after %d attempts: %w", maxRetries, lastErr)
}

// NEW: Helper function for creating PayPal order with retry
func createPayPalOrderWithRetry(ctx context.Context, accessToken string, orderData map[string]interface{}, maxRetries int) (map[string]interface{}, error) {
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		orderResponse, err := CreatePayPalOrder(accessToken, orderData)
		if err == nil {
			return orderResponse, nil
		}

		lastErr = err
		logger.LogWarn("PayPal order creation attempt %d failed: %v", attempt, err)

		if attempt < maxRetries {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
				// Exponential backoff
			}
		}
	}

	return nil, fmt.Errorf("failed to create PayPal order after %d attempts: %w", maxRetries, lastErr)
}

func capturePayPalOrderWithRetry(ctx context.Context, orderID, accessToken string, maxRetries int) (string, error) {
	captureURL := fmt.Sprintf("%s/v2/checkout/orders/%s/capture", config.APIBase(), orderID)

	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", captureURL, strings.NewReader("{}"))
		if err != nil {
			return "", fmt.Errorf("failed to create capture request: %w", err)
		}

		req.Header.Set("Authorization", accessToken)
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{Timeout: time.Second * 30}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			logger.LogWarn("PayPal capture attempt %d failed: %v", attempt, err)
			if attempt < maxRetries {
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(time.Duration(attempt) * time.Second):
					continue
				}
			}
			continue
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			lastErr = err
			logger.LogWarn("Failed to read capture response on attempt %d: %v", attempt, err)
			if attempt < maxRetries {
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			continue
		}

		if resp.StatusCode != http.StatusCreated {
			lastErr = fmt.Errorf("PayPal capture returned status %d: %s", resp.StatusCode, string(body))
			logger.LogWarn("PayPal capture attempt %d returned status %d", attempt, resp.StatusCode)
			if attempt < maxRetries {
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			continue
		}

		// Validate the capture was successful
		var captureData struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(body, &captureData); err == nil && captureData.Status == "COMPLETED" {
			logger.LogInfo("Successfully captured PayPal order %s on attempt %d", orderID, attempt)
			return string(body), nil
		}

		lastErr = fmt.Errorf("capture completed but status was not COMPLETED: %s", captureData.Status)
		logger.LogWarn("PayPal capture attempt %d completed but status was: %s", attempt, captureData.Status)

		if attempt < maxRetries {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}

	return "", fmt.Errorf("failed to capture PayPal order after %d attempts: %w", maxRetries, lastErr)
}
