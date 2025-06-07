// test_helpers.go - Updated with proper database initialization and concurrency handling
package testing

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"sbcbackend/internal/data"
	"sbcbackend/internal/inventory"
	"sbcbackend/internal/security"
)

// TestConfig holds configuration for test runs
type TestConfig struct {
	DBPath          string
	InventoryPath   string
	EnablePayPalAPI bool
	LogLevel        string
	TestDataDir     string
}

// TestSuite provides utilities for integration testing
type TestSuite struct {
	Config    TestConfig
	Server    *httptest.Server
	Client    *http.Client
	DB        *sql.DB // Direct DB handle for better control
	Inventory *inventory.Service
	mu        sync.Mutex
	testCount int
}

// NewTestSuite creates a new test suite with proper database setup
func NewTestSuite(t *testing.T) *TestSuite {
	// Create unique temporary directory for each test run
	testDir := filepath.Join(os.TempDir(), fmt.Sprintf("sbctest_%d_%d",
		time.Now().UnixNano(), os.Getpid()))
	if err := os.MkdirAll(testDir, 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}

	// Create unique temporary database for this test
	dbPath := filepath.Join(testDir, fmt.Sprintf("test_%d.db", time.Now().UnixNano()))

	// Create test inventory file
	inventoryPath := filepath.Join(testDir, "test_inventory.json")
	if err := createTestInventory(inventoryPath); err != nil {
		t.Fatalf("Failed to create test inventory: %v", err)
	}

	config := TestConfig{
		DBPath:          dbPath,
		InventoryPath:   inventoryPath,
		EnablePayPalAPI: false,   // Use mocks by default
		LogLevel:        "ERROR", // Reduce noise during tests
		TestDataDir:     testDir,
	}

	suite := &TestSuite{
		Config: config,
		Client: &http.Client{Timeout: 30 * time.Second},
	}

	// Initialize test database with proper concurrency settings
	if err := suite.InitDatabase(); err != nil {
		t.Fatalf("Failed to initialize test database: %v", err)
	}

	// Initialize inventory service
	suite.Inventory = inventory.NewService()
	if err := suite.Inventory.LoadInventory(inventoryPath); err != nil {
		t.Fatalf("Failed to load test inventory: %v", err)
	}

	t.Cleanup(func() {
		suite.Cleanup()
	})

	return suite
}

// InitDatabase sets up the test database with proper schema and settings
func (ts *TestSuite) InitDatabase() error {
	// Use the data package's initialization which should handle the SQLite driver
	if err := data.InitDB(ts.Config.DBPath); err != nil {
		return fmt.Errorf("failed to init data package: %w", err)
	}

	// Get the database connection from the data package
	db, err := data.GetDB()
	if err != nil {
		return fmt.Errorf("failed to get database connection: %w", err)
	}

	ts.DB = db

	// Test the connection
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("database ping failed: %w", err)
	}

	// Create all required tables with proper schema
	if err := ts.createTestSchema(ctx); err != nil {
		return fmt.Errorf("failed to create test schema: %w", err)
	}

	return nil
}

// createTestSchema creates the complete database schema for testing
func (ts *TestSuite) createTestSchema(ctx context.Context) error {
	schemas := []string{
		// Membership submissions table with all required columns
		`CREATE TABLE IF NOT EXISTS membership_submissions (
			form_id TEXT PRIMARY KEY,
			access_token TEXT NOT NULL,
			submission_date DATETIME NOT NULL,
			full_name TEXT NOT NULL,
			first_name TEXT,
			last_name TEXT,
			email TEXT NOT NULL,
			school TEXT,
			membership TEXT,
			membership_status TEXT,
			describe TEXT,
			student_count INTEGER DEFAULT 0,
			students_json TEXT,
			interests_json TEXT,
			addons_json TEXT,
			fees_json TEXT,
			donation REAL DEFAULT 0,
			calculated_amount REAL DEFAULT 0,
			cover_fees BOOLEAN DEFAULT 0,
			paypal_order_id TEXT,
			paypal_order_created_at DATETIME,
			paypal_status TEXT,
			paypal_details TEXT,
			submitted BOOLEAN DEFAULT 0,
			submitted_at DATETIME,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		// Event submissions table with has_food_orders column
		`CREATE TABLE IF NOT EXISTS event_submissions (
			form_id TEXT PRIMARY KEY,
			access_token TEXT NOT NULL,
			submission_date DATETIME NOT NULL,
			event TEXT NOT NULL,
			full_name TEXT NOT NULL,
			first_name TEXT,
			last_name TEXT,
			email TEXT NOT NULL,
			school TEXT,
			student_count INTEGER DEFAULT 0,
			students_json TEXT,
			food_choices_json TEXT,
			has_food_orders BOOLEAN DEFAULT 0,
			food_order_id TEXT,
			calculated_amount REAL DEFAULT 0,
			cover_fees BOOLEAN DEFAULT 0,
			paypal_order_id TEXT,
			paypal_order_created_at DATETIME,
			paypal_status TEXT,
			paypal_details TEXT,
			submitted BOOLEAN DEFAULT 0,
			submitted_at DATETIME,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		// Fundraiser submissions table
		`CREATE TABLE IF NOT EXISTS fundraiser_submissions (
			form_id TEXT PRIMARY KEY,
			access_token TEXT NOT NULL,
			submission_date DATETIME NOT NULL,
			full_name TEXT NOT NULL,
			first_name TEXT,
			last_name TEXT,
			email TEXT NOT NULL,
			school TEXT,
			describe TEXT,
			donor_status TEXT,
			student_count INTEGER DEFAULT 0,
			students_json TEXT,
			donation_items_json TEXT,
			total_amount REAL DEFAULT 0,
			calculated_amount REAL DEFAULT 0,
			cover_fees BOOLEAN DEFAULT 0,
			paypal_order_id TEXT,
			paypal_order_created_at DATETIME,
			paypal_status TEXT,
			paypal_details TEXT,
			submitted BOOLEAN DEFAULT 0,
			submitted_at DATETIME,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,

		// Create indexes for better performance
		`CREATE INDEX IF NOT EXISTS idx_membership_email ON membership_submissions(email)`,
		`CREATE INDEX IF NOT EXISTS idx_membership_submitted_at ON membership_submissions(submitted_at)`,
		`CREATE INDEX IF NOT EXISTS idx_event_email ON event_submissions(email)`,
		`CREATE INDEX IF NOT EXISTS idx_event_submitted_at ON event_submissions(submitted_at)`,
		`CREATE INDEX IF NOT EXISTS idx_fundraiser_email ON fundraiser_submissions(email)`,
		`CREATE INDEX IF NOT EXISTS idx_fundraiser_submitted_at ON fundraiser_submissions(submitted_at)`,
	}

	for _, schema := range schemas {
		if _, err := ts.DB.ExecContext(ctx, schema); err != nil {
			return fmt.Errorf("failed to create schema: %w", err)
		}
	}

	return nil
}

// Cleanup removes temporary test files and closes database
func (ts *TestSuite) Cleanup() {
	// Close database connection first
	if ts.DB != nil {
		ts.DB.Close()
	}

	// Close data package database
	if err := data.CloseDB(); err != nil {
		fmt.Printf("Warning: failed to close data package database: %v\n", err)
	}

	// Wait a moment for file handles to be released
	time.Sleep(200 * time.Millisecond)

	// Remove test directory
	if err := os.RemoveAll(ts.Config.TestDataDir); err != nil {
		fmt.Printf("Warning: failed to cleanup test directory %s: %v\n", ts.Config.TestDataDir, err)
	}
}

// ExecuteWithRetry executes a database operation with retry logic for BUSY errors
func (ts *TestSuite) ExecuteWithRetry(operation func() error, maxRetries int) error {
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if err := operation(); err != nil {
			lastErr = err
			// Check if it's a BUSY error
			if isBusyError(err) {
				// Exponential backoff with jitter
				backoff := time.Duration(i+1) * 10 * time.Millisecond
				time.Sleep(backoff)
				continue
			}
			return err // Non-BUSY error, don't retry
		}
		return nil // Success
	}
	return fmt.Errorf("operation failed after %d retries: %w", maxRetries, lastErr)
}

// isBusyError checks if an error is a SQLite BUSY error
func isBusyError(err error) bool {
	return err != nil && (err.Error() == "database is locked" ||
		err.Error() == "database is locked (5) (SQLITE_BUSY)" ||
		err.Error() == "database table is locked")
}

// GenerateFormID creates a test form ID
func (ts *TestSuite) GenerateFormID(formType string) string {
	ts.mu.Lock()
	ts.testCount++
	count := ts.testCount
	ts.mu.Unlock()

	return fmt.Sprintf("%s-test-%d-%d", formType, time.Now().Unix(), count)
}

// GenerateAccessToken creates a test access token
func (ts *TestSuite) GenerateAccessToken(formID, formType string) (string, error) {
	token, err := security.GenerateAccessToken()
	if err != nil {
		return "", err
	}

	security.StoreAccessToken(token, formID, formType)
	return token, nil
}

// MakeAPIRequest makes an authenticated API request
func (ts *TestSuite) MakeAPIRequest(method, path string, body interface{}, token string) (*http.Response, error) {
	var reqBody *bytes.Buffer

	if body != nil {
		bodyBytes, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewBuffer(bodyBytes)
	} else {
		reqBody = bytes.NewBuffer(nil)
	}

	req, err := http.NewRequest(method, ts.Server.URL+path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Access-Token", token)
	}

	return ts.Client.Do(req)
}

// ParseJSONResponse parses a JSON response into the provided interface
func (ts *TestSuite) ParseJSONResponse(resp *http.Response, dest interface{}) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(dest)
}

// AssertStatusCode checks if response has expected status code
func (ts *TestSuite) AssertStatusCode(t *testing.T, resp *http.Response, expected int) {
	t.Helper()
	if resp.StatusCode != expected {
		t.Errorf("Expected status code %d, got %d", expected, resp.StatusCode)
	}
}

// AssertNoError fails the test if error is not nil
func (ts *TestSuite) AssertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
}

// AssertError fails the test if error is nil
func (ts *TestSuite) AssertError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("Expected error but got nil")
	}
}

// WaitForCondition waits for a condition to be true or timeout
func (ts *TestSuite) WaitForCondition(condition func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// createTestInventory creates a test inventory.json file with corrected prices
func createTestInventory(path string) error {
	inventory := map[string]interface{}{
		"memberships": []map[string]interface{}{
			{"id": "basic", "name": "Basic Membership", "price": 25.0, "available": true},
			{"id": "premium", "name": "Premium Membership", "price": 50.0, "available": true},
			{"id": "gold", "name": "Gold Membership", "price": 100.0, "available": true},
		},
		"products": []map[string]interface{}{
			{"id": "tshirt", "name": "T-Shirt", "price": 15.0, "available": true},
			{"id": "stickers", "name": "Sticker Pack", "price": 5.0, "available": true},
		},
		"fees": []map[string]interface{}{
			{"id": "spring-festival", "name": "Spring Festival Fee", "price": 25.0, "available": true},
			{"id": "fall-festival", "name": "Fall Festival Fee", "price": 20.0, "available": true},
		},
		"events": map[string]interface{}{
			"spring-festival": map[string]interface{}{
				"per_student_options": map[string]interface{}{
					"registration": map[string]interface{}{
						"label": "Festival Registration", "price": 25.0, "required": true,
					},
					"lunch": map[string]interface{}{
						"label": "Lunch", "price": 10.0, "is_food": true,
					},
				},
				"shared_options": map[string]interface{}{
					"program": map[string]interface{}{
						"label": "Program Book", "price": 5.0, "max_quantity": 5,
					},
				},
			},
		},
		"processing_fees": map[string]interface{}{
			"rate":      0.029, // 2.9%
			"fixed_fee": 0.30,  // $0.30
		},
	}

	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(inventory)
}
