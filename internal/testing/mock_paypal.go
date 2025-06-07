// mock_paypal.go - Fixed with proper failure simulation
package testing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// MockPayPalService provides a mock PayPal API for testing
type MockPayPalService struct {
	Server          *httptest.Server
	Orders          map[string]*MockOrder
	AccessTokens    map[string]*MockAccessToken
	WebhookEndpoint string
	mu              sync.RWMutex

	// Configuration for failure simulation
	ShouldFailAuth        bool
	ShouldFailOrderCreate bool
	ShouldFailCapture     bool
	SimulateNetworkDelay  time.Duration

	// Counters for tracking
	AuthAttempts    int
	OrderAttempts   int
	CaptureAttempts int
}

type MockOrder struct {
	ID       string
	Status   string
	Amount   string
	FormID   string
	Created  time.Time
	Captured *time.Time
}

type MockAccessToken struct {
	Token     string
	ExpiresAt time.Time
}

// NewMockPayPalService creates a new mock PayPal service
func NewMockPayPalService() *MockPayPalService {
	mock := &MockPayPalService{
		Orders:       make(map[string]*MockOrder),
		AccessTokens: make(map[string]*MockAccessToken),
	}

	// Create HTTP server with PayPal API endpoints
	mux := http.NewServeMux()

	// OAuth token endpoint
	mux.HandleFunc("/v1/oauth2/token", mock.handleToken)

	// Order creation endpoint
	mux.HandleFunc("/v2/checkout/orders", mock.handleOrders)

	// Order details endpoint (dynamic route)
	mux.HandleFunc("/v2/checkout/orders/", mock.handleOrderDetails)

	mock.Server = httptest.NewServer(mux)
	return mock
}

// Close shuts down the mock server
func (m *MockPayPalService) Close() {
	m.Server.Close()
}

// GetAPIBase returns the mock server's base URL
func (m *MockPayPalService) GetAPIBase() string {
	return m.Server.URL
}

// CreateOrder creates a mock PayPal order
func (m *MockPayPalService) CreateOrder(formID, amount string) (*MockOrder, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.OrderAttempts++

	// Simulate network delay if configured
	if m.SimulateNetworkDelay > 0 {
		time.Sleep(m.SimulateNetworkDelay)
	}

	if m.ShouldFailOrderCreate {
		return nil, fmt.Errorf("mock: order creation failed")
	}

	orderID := fmt.Sprintf("MOCK-ORDER-%d", time.Now().UnixNano())
	order := &MockOrder{
		ID:      orderID,
		Status:  "CREATED",
		Amount:  amount,
		FormID:  formID,
		Created: time.Now(),
	}

	m.Orders[orderID] = order
	return order, nil
}

// CaptureOrder captures a mock PayPal order
func (m *MockPayPalService) CaptureOrder(orderID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.CaptureAttempts++

	// Simulate network delay if configured
	if m.SimulateNetworkDelay > 0 {
		time.Sleep(m.SimulateNetworkDelay)
	}

	if m.ShouldFailCapture {
		return fmt.Errorf("mock: capture failed")
	}

	order, exists := m.Orders[orderID]
	if !exists {
		return fmt.Errorf("order not found: %s", orderID)
	}

	order.Status = "COMPLETED"
	now := time.Now()
	order.Captured = &now
	return nil
}

// GetOrder retrieves a mock order
func (m *MockPayPalService) GetOrder(orderID string) (*MockOrder, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	order, exists := m.Orders[orderID]
	return order, exists
}

// HTTP Handlers

func (m *MockPayPalService) handleToken(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.AuthAttempts++
	shouldFail := m.ShouldFailAuth
	delay := m.SimulateNetworkDelay
	m.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}

	if shouldFail {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":             "invalid_client",
			"error_description": "Authentication failed",
		})
		return
	}

	// Generate mock access token
	token := fmt.Sprintf("mock-token-%d", time.Now().UnixNano())

	m.mu.Lock()
	m.AccessTokens[token] = &MockAccessToken{
		Token:     token,
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}
	m.mu.Unlock()

	response := map[string]interface{}{
		"access_token": token,
		"token_type":   "Bearer",
		"expires_in":   3600,
		"scope":        "https://uri.paypal.com/services/checkout/one-click-with-merchant-issued-token",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (m *MockPayPalService) handleOrders(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	delay := m.SimulateNetworkDelay
	m.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}

	switch r.Method {
	case "POST":
		m.handleCreateOrder(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (m *MockPayPalService) handleCreateOrder(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.OrderAttempts++
	shouldFail := m.ShouldFailOrderCreate
	m.mu.Unlock()

	if shouldFail {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":   "INTERNAL_SERVER_ERROR",
			"message": "Order creation failed",
		})
		return
	}

	var orderRequest map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&orderRequest); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Extract amount and formID
	purchaseUnits, ok := orderRequest["purchase_units"].([]interface{})
	if !ok || len(purchaseUnits) == 0 {
		http.Error(w, "Invalid purchase units", http.StatusBadRequest)
		return
	}

	unit, ok := purchaseUnits[0].(map[string]interface{})
	if !ok {
		http.Error(w, "Invalid purchase unit", http.StatusBadRequest)
		return
	}

	amount, _ := unit["amount"].(map[string]interface{})
	value, _ := amount["value"].(string)
	formID, _ := unit["invoice_id"].(string)

	// Create mock order
	order, err := m.CreateOrder(formID, value)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Return PayPal-like response
	response := map[string]interface{}{
		"id":     order.ID,
		"status": order.Status,
		"links": []map[string]interface{}{
			{
				"href":   fmt.Sprintf("%s/v2/checkout/orders/%s", m.Server.URL, order.ID),
				"rel":    "self",
				"method": "GET",
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(response)
}

func (m *MockPayPalService) handleOrderDetails(w http.ResponseWriter, r *http.Request) {
	// Extract order ID from path
	path := strings.TrimPrefix(r.URL.Path, "/v2/checkout/orders/")
	pathParts := strings.Split(path, "/")
	orderID := pathParts[0]

	switch r.Method {
	case "GET":
		m.handleGetOrder(w, r, orderID)
	case "POST":
		if len(pathParts) > 1 && pathParts[1] == "capture" {
			m.handleCaptureOrder(w, r, orderID)
		} else {
			http.Error(w, "Invalid endpoint", http.StatusNotFound)
		}
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (m *MockPayPalService) handleGetOrder(w http.ResponseWriter, r *http.Request, orderID string) {
	order, exists := m.GetOrder(orderID)
	if !exists {
		http.Error(w, "Order not found", http.StatusNotFound)
		return
	}

	response := map[string]interface{}{
		"id":     order.ID,
		"status": order.Status,
		"purchase_units": []map[string]interface{}{
			{
				"invoice_id": order.FormID,
				"amount": map[string]interface{}{
					"currency_code": "USD",
					"value":         order.Amount,
				},
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (m *MockPayPalService) handleCaptureOrder(w http.ResponseWriter, r *http.Request, orderID string) {
	m.mu.Lock()
	m.CaptureAttempts++
	shouldFail := m.ShouldFailCapture
	delay := m.SimulateNetworkDelay
	m.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}

	if shouldFail {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":   "INTERNAL_SERVER_ERROR",
			"message": "Capture failed",
		})
		return
	}

	if err := m.CaptureOrder(orderID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	order, _ := m.GetOrder(orderID)

	response := map[string]interface{}{
		"id":     order.ID,
		"status": "COMPLETED",
		"purchase_units": []map[string]interface{}{
			{
				"payments": map[string]interface{}{
					"captures": []map[string]interface{}{
						{
							"id":     fmt.Sprintf("CAPTURE-%s", order.ID),
							"status": "COMPLETED",
							"amount": map[string]interface{}{
								"currency_code": "USD",
								"value":         order.Amount,
							},
						},
					},
				},
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(response)
}

// Test Utilities

// SetFailureMode configures the mock to simulate various failure scenarios
func (m *MockPayPalService) SetFailureMode(authFail, createFail, captureFail bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.ShouldFailAuth = authFail
	m.ShouldFailOrderCreate = createFail
	m.ShouldFailCapture = captureFail
}

// SetNetworkDelay simulates network latency
func (m *MockPayPalService) SetNetworkDelay(delay time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.SimulateNetworkDelay = delay
}

// GetOrderCount returns the number of orders created
func (m *MockPayPalService) GetOrderCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return len(m.Orders)
}

// GetCompletedOrderCount returns the number of completed orders
func (m *MockPayPalService) GetCompletedOrderCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count := 0
	for _, order := range m.Orders {
		if order.Status == "COMPLETED" {
			count++
		}
	}
	return count
}

// GetStats returns statistics about mock usage
func (m *MockPayPalService) GetStats() map[string]int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return map[string]int{
		"auth_attempts":    m.AuthAttempts,
		"order_attempts":   m.OrderAttempts,
		"capture_attempts": m.CaptureAttempts,
		"total_orders":     len(m.Orders),
		"completed_orders": m.GetCompletedOrderCount(),
	}
}

// Reset clears all mock data
func (m *MockPayPalService) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Orders = make(map[string]*MockOrder)
	m.AccessTokens = make(map[string]*MockAccessToken)
	m.ShouldFailAuth = false
	m.ShouldFailOrderCreate = false
	m.ShouldFailCapture = false
	m.SimulateNetworkDelay = 0
	m.AuthAttempts = 0
	m.OrderAttempts = 0
	m.CaptureAttempts = 0
}
