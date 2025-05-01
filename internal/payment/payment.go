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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"sbcbackend/internal/config"
	"sbcbackend/internal/data"
	"sbcbackend/internal/logger"
	"sbcbackend/internal/security"
)

const (
	paypalAPIBaseSandbox = "https://api.sandbox.paypal.com" // Not used, in config
	paypalAPIBaseLive    = "https://api.paypal.com"         // Not used, in config
)

const (
	unmatchedPaymentsDir = "/home/protected/boosterbackend/data/unmatched-payments"
)

var pendingPayments = make(map[string]PendingPayment)
var pendingPaymentsMutex sync.Mutex
var timeZone *time.Location

type PendingPayment struct {
	FormID      string   `json:"formID"`
	OrderID     string   `json:"orderID,omitempty"`
	OrderStatus string   `json:"orderStatus,omitempty"`
	PayerName   string   `json:"payerName,omitempty"`
	PayerEmail  string   `json:"payerEmail,omitempty"`
	Amount      float64  `json:"calculated_amount"`
	Currency    string   `json:"currency,omitempty"` // Default to USD if missing
	Membership  string   `json:"membership"`
	Addons      []string `json:"addons,omitempty"`
	CreatedAt   string   `json:"createdAt"`

	// Extra fields from the form JSON
	Donation         float64 `json:"donation,omitempty"`
	CalculatedAmount float64 `json:"calculated_amount,omitempty"`
	FullName         string  `json:"full_name,omitempty"`
	FirstName        string  `json:"first_name,omitempty"`
	LastName         string  `json:"last_name,omitempty"`
	School           string  `json:"school,omitempty"`
	StudentCount     string  `json:"student_count,omitempty"`
	FormType         string  `json:"form_type,omitempty"`
	SubmissionDate   string  `json:"submission_date,omitempty"`
}

type PaymentDetails struct {
	Amount     float64 `json:"calculated_amount"`
	Membership string  `json:"membership"`
	// Add more fields if needed
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
}

// PreloadPendingPayments scans the data directory for form JSON files and loads them into memory.
func PreloadPendingPayments() error {
	pendingPaymentsMutex.Lock()
	defer pendingPaymentsMutex.Unlock()

	year := time.Now().Year()
	baseDir := config.GetFormsDataDirectory()
	dirPath := filepath.Join(baseDir, fmt.Sprintf("%d", year))

	// === FIX: Ensure directory exists ===
	if err := os.MkdirAll(dirPath, 0775); err != nil {
		return fmt.Errorf("failed to create or access data directory: %v", err)
	}

	files, err := os.ReadDir(dirPath)
	if err != nil {
		return fmt.Errorf("failed to read data directory: %v", err)
	}

	// Iterate over all files in the directory
	for _, file := range files {
		if file.IsDir() {
			continue
		}

		// Check if the file is a valid membership file
		name := file.Name()
		if filepath.Ext(name) != ".json" || !strings.HasPrefix(name, "membership_") {
			continue
		}

		// Read the file
		filePath := filepath.Join(dirPath, name)
		fileBytes, err := os.ReadFile(filePath)
		if err != nil {
			logger.LogError("Failed to read file %s: %v", filePath, err)
			continue
		}

		// Unmarshal the JSON into a PendingPayment struct
		var pending PendingPayment
		err = json.Unmarshal(fileBytes, &pending)
		if err != nil {
			logger.LogError("Failed to parse JSON in %s: %v", filePath, err)
			continue
		}

		// Normalize the PendingPayment data (ensure required fields are populated)
		if err := validateAndNormalizePendingPayment(&pending); err != nil {
			logger.LogError("Failed to validate payment for formID %s: %v", name, err)
			continue
		}

		// Extract formID from the filename (strip "membership_" prefix and ".json" suffix)
		formID := strings.TrimSuffix(strings.TrimPrefix(name, "membership_"), ".json")

		// Add the loaded payment details to the map
		pendingPayments[formID] = pending
	}

	// Return success
	return nil
}

func GetPayPalAccessToken(ctx context.Context) (string, error) {
	// Use context-aware request creation
	authURL := fmt.Sprintf("%s/v1/oauth2/token", config.APIBase())

	// Use url.Values for form data (more idiomatic)
	formData := url.Values{}
	formData.Set("grant_type", "client_credentials")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return "", fmt.Errorf("creating PayPal auth request: %w", err)
	}

	req.SetBasicAuth(config.ClientID(), config.ClientSecret())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Configure a client with reasonable timeouts and appropriate transport settings
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12, // PayPal requires TLS 1.2 or higher
			},
			MaxIdleConns:        10,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  false,
			MaxIdleConnsPerHost: 5,
		},
	}

	logger.LogInfo("Requesting PayPal access token")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("executing PayPal auth request: %w", err)
	}
	defer resp.Body.Close()

	// Read body once for potential error reporting
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading PayPal response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		logger.LogError("PayPal API error (HTTP %d): %s", resp.StatusCode, string(body))
		return "", fmt.Errorf("PayPal API returned status %d: %s", resp.StatusCode, string(body))
	}

	// Try to parse successful response
	var result PayPalTokenResponse

	if err := json.Unmarshal(body, &result); err != nil {
		// If we couldn't parse the success response, try to parse as error
		if resp.StatusCode != http.StatusOK {
			var paypalError PayPalError
			if jsonErr := json.Unmarshal(body, &paypalError); jsonErr == nil {
				return "", fmt.Errorf("PayPal API error: %s (debug_id: %s)",
					paypalError.Message, paypalError.DebugID)
			}
		}
		return "", fmt.Errorf("parsing PayPal auth response: %w", err)
	}

	if result.AccessToken == "" {
		return "", fmt.Errorf("access token not found in PayPal response")
	}

	// Validate token type according to PayPal docs
	if result.TokenType != "Bearer" {
		logger.LogError("Unexpected token type from PayPal: %s", result.TokenType)
	}

	logger.LogInfo("Successfully obtained PayPal access token (app_id: %s, expires in %d seconds)",
		result.AppID, result.ExpiresIn)

	// Return the token with the required prefix for use in Authorization header
	return fmt.Sprintf("%s %s", result.TokenType, result.AccessToken), nil
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

// Improved CreatePayPalOrderHandler with better locking
func CreatePayPalOrderHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		logger.LogError("Invalid request method: %s", r.Method)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req OrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.LogError("Failed to decode JSON request body: %v", err)
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	if req.FormID == "" || req.Token == "" {
		http.Error(w, "Missing formID or token", http.StatusBadRequest)
		return
	}

	year := time.Now().Year()
	filePath := data.GetFilePathForForm(req.FormID, year)

	raw, err := os.ReadFile(filePath)
	if err != nil {
		logger.LogError("Could not read form file %s: %v", filePath, err)
		http.Error(w, "Order not found", http.StatusNotFound)
		return
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		logger.LogError("Could not parse form JSON for %s: %v", req.FormID, err)
		http.Error(w, "Corrupt order data", http.StatusInternalServerError)
		return
	}

	// Sanity check: payload must already contain required base fields
	if _, ok := payload["access_token"].(string); !ok {
		logger.LogError("Missing or invalid access_token in form JSON for %s", req.FormID)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if saved := payload["access_token"].(string); saved != req.Token {
		logger.LogError("Token mismatch for formID %s", req.FormID)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Short-circuit if PayPal order already exists
	if orderID, ok := payload["paypal_order_id"].(string); ok && orderID != "" {
		logger.LogInfo("Reusing existing PayPal order for formID %s (OrderID: %s)", req.FormID, orderID)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"id": orderID})
		return
	}

	// Load payment details
	paymentDetails, err := GetPaymentDetails(req.FormID, true)
	if err != nil {
		logger.LogError("Failed to retrieve payment details for formID %s: %v", req.FormID, err)
		http.Error(w, "Invalid form ID", http.StatusBadRequest)
		return
	}

	orderData := map[string]interface{}{
		"intent": "CAPTURE",
		"purchase_units": []map[string]interface{}{
			{
				"amount": map[string]interface{}{
					"currency_code": "USD",
					"value":         fmt.Sprintf("%.2f", paymentDetails.Amount),
				},
				"description": paymentDetails.Membership,
				"invoice_id":  req.FormID,
			},
		},
	}

	accessToken, err := GetPayPalAccessToken(r.Context())
	if err != nil {
		logger.LogError("Unable to retrieve PayPal access token: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	orderResponse, err := CreatePayPalOrder(accessToken, orderData)
	if err != nil {
		logger.LogError("Order creation failed for formID %s: %v", req.FormID, err)
		http.Error(w, "Order creation failed", http.StatusInternalServerError)
		return
	}

	id, ok := orderResponse["id"].(string)
	if !ok || id == "" {
		logger.LogError("Missing PayPal order ID in response: %+v", orderResponse)
		http.Error(w, "Invalid PayPal response", http.StatusInternalServerError)
		return
	}

	// Safely update form with new order ID
	payload["paypal_order_id"] = id
	payload["paypal_order_created_at"] = time.Now().Format(time.RFC3339)

	if err := data.WriteFileSafely(filePath, payload); err != nil {
		logger.LogError("Failed to save updated PayPal order ID for %s: %v", req.FormID, err)
		// Proceed with returning the order ID even if write failed
	}

	logger.LogInfo("PayPal order %s successfully created and stored for formID %s", id, req.FormID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": id})
}

// Improved CapturePayPalOrderHandler with idempotency
func CapturePayPalOrderHandler(w http.ResponseWriter, r *http.Request) {
	logger.LogHTTPRequest(r)

	if r.Method != http.MethodPost {
		logger.LogWarn("Capture request used invalid method: %s", r.Method)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var input struct {
		OrderID string `json:"orderID"`
		FormID  string `json:"formID"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		logger.LogError("Failed to decode JSON body in capture handler: %v", err)
		http.Error(w, "Invalid input", http.StatusBadRequest)
		return
	}
	logger.LogInfo("Capture request received for OrderID: %s, FormID: %s", input.OrderID, input.FormID)

	if input.OrderID == "" {
		logger.LogError("OrderID missing in capture request")
		http.Error(w, "Missing orderID", http.StatusBadRequest)
		return
	}

	// Step 1: Check for already completed order
	if input.FormID != "" {
		year := time.Now().Year()
		filePath := data.GetFilePathForForm(input.FormID, year)
		logger.LogInfo("Checking form file at path: %s", filePath)

		fileBytes, err := os.ReadFile(filePath)
		if err == nil {
			var formData map[string]interface{}
			if err := json.Unmarshal(fileBytes, &formData); err == nil {
				if details, ok := formData["paypal_details"].(map[string]interface{}); ok {
					if status, ok := details["status"].(string); ok && status == "COMPLETED" {
						logger.LogInfo("Order %s already marked as COMPLETED for formID %s", input.OrderID, input.FormID)
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(map[string]string{
							"status":  "COMPLETED",
							"message": "Order already processed",
						})
						return
					}
				}
			}
		}
	}

	// Step 2: Perform capture
	accessToken, err := GetPayPalAccessToken(r.Context())
	if err != nil {
		logger.LogError("Failed to retrieve PayPal access token: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	captureURL := fmt.Sprintf("%s/v2/checkout/orders/%s/capture", config.APIBase(), input.OrderID)
	logger.LogInfo("Sending capture request to PayPal: %s", captureURL)

	req, err := http.NewRequest("POST", captureURL, strings.NewReader("{}"))
	if err != nil {
		logger.LogError("Failed to construct capture request: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Authorization", accessToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		logger.LogError("Capture request failed: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.LogError("Error reading capture response body: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if resp.StatusCode != http.StatusCreated {
		logger.LogError("Capture failed (HTTP %d): %s", resp.StatusCode, string(body))
		http.Error(w, "Failed to capture order", http.StatusInternalServerError)
		return
	}
	logger.LogInfo("Capture response for OrderID %s: %s", input.OrderID, string(body))

	// Step 3: Update form file
	if input.FormID != "" {
		filePath := data.GetFilePathForForm(input.FormID, time.Now().Year())
		logger.LogInfo("Attempting to update form file with capture data: %s", filePath)

		var captureData map[string]interface{}
		if err := json.Unmarshal(body, &captureData); err != nil {
			logger.LogError("Failed to parse capture response JSON: %v", err)
		} else {
			fileBytes, err := os.ReadFile(filePath)
			if err != nil {
				logger.LogError("Failed to read form file for updating: %v", err)
			} else {
				var formData map[string]interface{}
				if err := json.Unmarshal(fileBytes, &formData); err != nil {
					logger.LogError("Failed to parse form JSON: %v", err)
				} else {
					if formData["paypal_details"] == nil {
						formData["paypal_details"] = make(map[string]interface{})
					}
					details, _ := formData["paypal_details"].(map[string]interface{})
					details["capture_response"] = captureData
					details["status"] = "COMPLETED"
					details["captured_at"] = time.Now().Format(time.RFC3339)

					if err := data.WriteFileSafely(filePath, formData); err != nil { // Pass formData directly
						logger.LogError("Failed to write updated form file: %v", err)
					} else {
						logger.LogInfo("Successfully updated form file with capture details")
					}
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

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

	// Verify webhook signature
	if !verifyPayPalWebhookSignature(
		r.Header.Get("Paypal-Transmission-Id"),
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

	// Parse event payload
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

	year := time.Now().Year()
	filePath := data.GetFilePathForForm(formID, year)

	formData, err := loadFormData(filePath, year)
	if err != nil {
		logger.LogError("Could not load form data: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	updatePayPalDetails(formData, eventType, resource)

	if err := saveFormData(filePath, formData); err != nil {
		logger.LogError("Could not save updated form data: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	logger.LogInfo("Successfully updated form %s with webhook event %s", formID, eventType)
	w.WriteHeader(http.StatusOK)
}

func extractFormIDFromResource(resource map[string]interface{}) string {
	if purchaseUnits, ok := resource["purchase_units"].([]interface{}); ok && len(purchaseUnits) > 0 {
		if unit, ok := purchaseUnits[0].(map[string]interface{}); ok {
			formID, _ := unit["invoice_id"].(string)
			return formID
		}
	}
	// Some captures have invoice_id directly at top level
	if invoiceID, ok := resource["invoice_id"].(string); ok {
		return invoiceID
	}
	return ""
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

func loadFormData(formID string, year int) (map[string]interface{}, error) {
	year = time.Now().Year()
	filePath := data.GetFilePathForForm(formID, year) // Now using both arguments
	dataBytes, err := os.ReadFile(filePath)           // Assuming you want to read the file at this path
	if err != nil {
		return nil, err
	}
	var formData map[string]interface{}
	if err := json.Unmarshal(dataBytes, &formData); err != nil {
		return nil, err
	}
	return formData, nil
}

func saveFormData(filePath string, formData map[string]interface{}) error {
	dataBytes, err := json.MarshalIndent(formData, "", "  ")
	if err != nil {
		return err
	}
	return data.WriteFileSafely(filePath, dataBytes)
}

// verifyPayPalWebhookSignature verifies the signature of a PayPal webhook.
func verifyPayPalWebhookSignature(
	transmissionID, transmissionSig, transmissionTime, certURL, authAlgo string,
	payload []byte,
) bool {
	ctx := context.Background()

	// Step 1: Get PayPal access token
	accessToken, err := GetPayPalAccessToken(ctx)
	if err != nil {
		logger.LogError("Failed to get access token for webhook verification: %v", err)
		return false
	}

	// Step 2: Build the verification payload
	requestBody := map[string]interface{}{
		"auth_algo":         authAlgo,
		"cert_url":          certURL,
		"transmission_id":   transmissionID,
		"transmission_sig":  transmissionSig,
		"transmission_time": transmissionTime,
		"webhook_id":        config.PayPalWebhookID, // must match the one configured in PayPal dev portal
		"webhook_event":     json.RawMessage(payload),
	}

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		logger.LogError("Failed to marshal webhook verification payload: %v", err)
		return false
	}

	// Step 3: Send verification request to PayPal
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
		logger.LogError("Failed to decode webhook verification response: %v", err)
		return false
	}

	logger.LogInfo("Webhook verification status: %s", result.VerificationStatus)
	return result.VerificationStatus == "SUCCESS"
}

// SavePaymentDataHandler handles saving payment data.
func SavePaymentDataHandler(w http.ResponseWriter, r *http.Request) {
	logger.LogHTTPRequest(r)

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var input struct {
		FormID     string   `json:"formID"`
		Amount     float64  `json:"amount"` // Unused for calculation, for reference only
		Membership string   `json:"membership"`
		Addons     []string `json:"addons"`
		Donation   float64  `json:"donation"`
		CoverFees  bool     `json:"cover_fees"`
	}

	err := json.NewDecoder(r.Body).Decode(&input)
	if err != nil {
		logger.LogError("Invalid JSON body: %v", err)
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if input.FormID == "" {
		logger.LogError("Missing formID in request")
		http.Error(w, "Missing form ID", http.StatusBadRequest)
		return
	}
	logger.LogInfo("Processing payment form: %s", input.FormID)

	// Load membership names (validation only)
	membershipsPath := config.GetEnvBasedSetting("MEMBERSHIPS_JSON_PATH")
	if membershipsPath == "" {
		membershipsPath = "/home/public/static/memberships.json"
	}
	validMemberships, err := data.LoadValidNames(membershipsPath)
	if err != nil {
		logger.LogError("Failed to load memberships from %s: %v", membershipsPath, err)
		http.Error(w, "Failed to load memberships", http.StatusInternalServerError)
		return
	}
	if !validMemberships[input.Membership] {
		logger.LogError("Invalid membership selection: %s", input.Membership)
		http.Error(w, "Invalid membership", http.StatusBadRequest)
		return
	}

	// Load product names (validation only)
	productsPath := config.GetEnvBasedSetting("PRODUCTS_JSON_PATH")
	if productsPath == "" {
		productsPath = "/home/public/static/products.json"
	}
	validProducts, err := data.LoadValidNames(productsPath)
	if err != nil {
		logger.LogError("Failed to load products from %s: %v", productsPath, err)
		http.Error(w, "Failed to load products", http.StatusInternalServerError)
		return
	}
	for _, addon := range input.Addons {
		if !validProducts[addon] {
			logger.LogError("Invalid addon selection: %s", addon)
			http.Error(w, "Invalid addon: "+addon, http.StatusBadRequest)
			return
		}
	}

	// Build file path and load existing form data
	year := time.Now().Year()
	filePath := data.GetFilePathForForm(input.FormID, year)
	logger.LogInfo("Saving payment data to: %s", filePath)

	fileBytes, err := os.ReadFile(filePath)
	if err != nil {
		logger.LogError("Failed to read form file %s: %v", filePath, err)
		http.Error(w, "Failed to read form file", http.StatusInternalServerError)
		return
	}

	var formData map[string]interface{}
	if err := json.Unmarshal(fileBytes, &formData); err != nil {
		logger.LogError("Failed to parse form JSON %s: %v", filePath, err)
		http.Error(w, "Failed to parse form data", http.StatusInternalServerError)
		return
	}

	// --- NEW: Prevent resubmission ---
	if submitted, ok := formData["submitted"].(bool); ok && submitted {
		logger.LogError("Attempt to resubmit already submitted formID: %s", input.FormID)
		http.Error(w, "This form has already been submitted", http.StatusConflict)
		return
	}
	// --- END NEW ---

	// Load prices for calculations
	membershipPrices, err := data.LoadNamePriceMap(membershipsPath)
	if err != nil {
		logger.LogError("Failed to load membership prices from %s: %v", membershipsPath, err)
		http.Error(w, "Failed to load membership pricing", http.StatusInternalServerError)
		return
	}
	productPrices, err := data.LoadNamePriceMap(productsPath)
	if err != nil {
		logger.LogError("Failed to load product prices from %s: %v", productsPath, err)
		http.Error(w, "Failed to load product pricing", http.StatusInternalServerError)
		return
	}

	// Perform pricing calculation
	subtotal := membershipPrices[input.Membership]
	logger.LogInfo("Base membership '%s' = $%.2f", input.Membership, subtotal)

	for _, addon := range input.Addons {
		addonPrice := productPrices[addon]
		logger.LogInfo("Addon '%s' = $%.2f", addon, addonPrice)
		subtotal += addonPrice
	}

	if input.Donation > 0 {
		logger.LogInfo("Donation = $%.2f", input.Donation)
		subtotal += input.Donation
	}

	finalTotal := subtotal
	if input.CoverFees {
		fees := subtotal*0.02 + 0.49
		finalTotal += fees
		logger.LogInfo("Covering processing fees: +$%.2f", fees)
	}
	finalTotal = math.Round(finalTotal*100) / 100

	logger.LogInfo("Calculated total amount for %s: $%.2f", input.FormID, finalTotal)

	// Generate a random access token
	accessToken, err := security.GenerateAccessToken()
	if err != nil {
		logger.LogError("Failed to generate access token: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	formData["access_token"] = accessToken
	logger.LogInfo("Generated access token for %s: %s", input.FormID, accessToken)

	// Update form data
	formData["membership"] = input.Membership
	formData["addons"] = input.Addons
	formData["donation"] = input.Donation
	formData["cover_fees"] = input.CoverFees
	formData["calculated_amount"] = finalTotal
	formData["submitted"] = true
	formData["submitted_at"] = time.Now().Format(time.RFC3339)

	// Write updated JSON back to file
	file, err := os.Create(filePath)
	if err != nil {
		logger.LogError("Failed to open file for writing %s: %v", filePath, err)
		http.Error(w, "Failed to write form data", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(formData); err != nil {
		logger.LogError("Failed to encode JSON to %s: %v", filePath, err)
		http.Error(w, "Failed to encode form data", http.StatusInternalServerError)
		return
	}

	// Track in-memory pending payments
	pendingPaymentsMutex.Lock()
	pendingPayments[input.FormID] = PendingPayment{
		FormID:     input.FormID,
		Amount:     finalTotal,
		Membership: input.Membership,
		Addons:     input.Addons,
		CreatedAt:  data.GetCurrentTimeInZone(timeZone),
	}
	pendingPaymentsMutex.Unlock()

	logger.LogInfo("[SavePaymentDataHandler] Successfully processed form %s", input.FormID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"formID":      input.FormID,
		"accessToken": accessToken,
	})
}

// GetPaymentDetails retrieves a PendingPayment for a given formID.
// It checks memory first, and optionally falls back to disk if not found.
// GetPaymentDetails retrieves payment details for a given formID.
// First it checks the in-memory pendingPayments map.
// If not found and fallbackToDisk is true, it attempts to load and validate the data from disk.
func GetPaymentDetails(formID string, fallbackToDisk bool) (PendingPayment, error) {
	// Lock to avoid race conditions with the pendingPayments map
	pendingPaymentsMutex.Lock()
	payment, ok := pendingPayments[formID]
	pendingPaymentsMutex.Unlock()

	// If found in the in-memory map, return the payment
	if ok {
		if err := validateAndNormalizePendingPayment(&payment); err != nil {
			return PendingPayment{}, fmt.Errorf("failed to validate pending payment for formID %s: %w", formID, err)
		}
		return payment, nil
	}

	// If fallbackToDisk is false and not found, return error
	if !fallbackToDisk {
		return PendingPayment{}, fmt.Errorf("payment details not found for formID: %s", formID)
	}

	// Try loading payment details from disk (if fallback is allowed)
	year := time.Now().Year()
	filePath := data.GetFilePathForForm(formID, year)

	// Read file bytes from disk
	fileBytes, err := os.ReadFile(filePath)
	if err != nil {
		return PendingPayment{}, fmt.Errorf("payment details not found for formID: %s", formID)
	}

	// Unmarshal the payment details into PendingPayment struct
	var pending PendingPayment
	err = json.Unmarshal(fileBytes, &pending)
	if err != nil {
		return PendingPayment{}, fmt.Errorf("failed to parse payment data for formID: %s", formID)
	}

	// Validate and normalize the loaded payment
	if err := validateAndNormalizePendingPayment(&pending); err != nil {
		return PendingPayment{}, fmt.Errorf("invalid pending payment for formID %s: %w", formID, err)
	}

	return pending, nil
}

// validateAndNormalizePendingPayment ensures that a PendingPayment object is usable.
// It fills missing fields where possible and checks basic data integrity.
func validateAndNormalizePendingPayment(pending *PendingPayment) error {
	if pending == nil {
		return fmt.Errorf("pending payment is nil")
	}

	// Basic validation
	if pending.FormID == "" {
		return fmt.Errorf("missing formID")
	}
	if pending.Membership == "" {
		return fmt.Errorf("missing membership")
	}
	if pending.Amount <= 0 {
		return fmt.Errorf("invalid amount: %.2f", pending.Amount)
	}

	// Normalize currency
	if pending.Currency == "" {
		pending.Currency = "USD"
	}

	// Normalize createdAt
	if pending.CreatedAt == "" {
		pending.CreatedAt = time.Now().Format(time.RFC3339)
	}

	// Normalize calculated amount if missing but donation exists
	if pending.CalculatedAmount == 0 && pending.Donation > 0 {
		pending.CalculatedAmount = pending.Amount + pending.Donation
	}

	// Normalize full name if missing
	if pending.FullName == "" {
		if pending.FirstName != "" && pending.LastName != "" {
			pending.FullName = pending.FirstName + " " + pending.LastName
		}
	}

	// Normalize payer name if missing
	if pending.PayerName == "" && pending.FullName != "" {
		pending.PayerName = pending.FullName
	}

	return nil
}

// GetPaymentDetailsHandler is the HTTP handler for getting payment details.
func GetPaymentDetailsHandler(w http.ResponseWriter, r *http.Request) {
	logger.LogHTTPRequest(r)

	formID := r.URL.Query().Get("formID")
	token := r.URL.Query().Get("token")

	if formID == "" || token == "" {
		logger.LogError("Missing formID or token in request")
		http.Error(w, "Missing formID or token", http.StatusBadRequest)
		return
	}

	year := time.Now().Year()
	filePath := data.GetFilePathForForm(formID, year)

	fileBytes, err := os.ReadFile(filePath)
	if err != nil {
		logger.LogError("Failed to read payment file for formID %s: %v", formID, err)
		http.Error(w, "Payment details not found", http.StatusNotFound)
		return
	}

	var fullData map[string]interface{}
	if err := json.Unmarshal(fileBytes, &fullData); err != nil {
		logger.LogError("Failed to parse form JSON for formID %s: %v", formID, err)
		http.Error(w, "Failed to parse form data", http.StatusInternalServerError)
		return
	}

	// Check access token
	savedToken, ok := fullData["access_token"].(string)
	if !ok {
		logger.LogError("Access token missing in saved form data for formID %s", formID)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if savedToken != token {
		logger.LogError("Access token mismatch for formID %s", formID)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	logger.LogInfo("Successfully verified access token for formID %s", formID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(fullData)
}
