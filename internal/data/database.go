package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"sbcbackend/internal/logger"
)

// =============================================================================
// CONSTANTS AND GLOBAL VARIABLES
// =============================================================================

// Global database instance with better management
var (
	db     *sql.DB
	dbMu   sync.RWMutex
	dbInit sync.Once
)

// Database connection pool configuration
const (
	maxOpenConns    = 25
	maxIdleConns    = 5
	connMaxLifetime = time.Hour
	connMaxIdleTime = time.Minute * 15
	queryTimeout    = time.Second * 30
)

const TimeFormat = time.RFC3339

// =============================================================================
// STRUCT DEFINITIONS (ALL TYPES)
// =============================================================================

// Core types
// type Database struct
type Student struct {
	Name  string `json:"name"`
	Grade string `json:"grade"`
}

// Form submission types

type MembershipSubmission struct {
	FormID               string
	AccessToken          string
	SubmissionDate       time.Time
	FullName             string
	FirstName            string
	LastName             string
	Email                string
	School               string
	Membership           string
	MembershipStatus     string
	Interests            []string
	Describe             string
	StudentCount         int
	Students             []Student
	Fees                 map[string]int
	Addons               []string
	Donation             float64
	CalculatedAmount     float64
	CoverFees            bool
	PayPalOrderID        string
	PayPalOrderCreatedAt *time.Time
	PayPalStatus         string
	PayPalDetails        string
	Submitted            bool
	SubmittedAt          *time.Time

	// ADD these new computed fields for PayPal data:
	PayPalEmail      string  `json:"paypal_email,omitempty"`
	PayPalCaptureID  string  `json:"paypal_capture_id,omitempty"`
	PayPalCaptureURL string  `json:"paypal_capture_url,omitempty"`
	PayPalFee        float64 `json:"paypal_fee,omitempty"`

	// NEW email fields
	ConfirmationEmailSent   bool
	ConfirmationEmailSentAt *time.Time
	AdminNotificationSent   bool
	AdminNotificationSentAt *time.Time
}

type EventSubmission struct {
	FormID               string
	AccessToken          string
	SubmissionDate       time.Time
	Event                string
	FullName             string
	FirstName            string
	LastName             string
	Email                string
	School               string
	StudentCount         int
	Students             []Student
	Submitted            bool
	SubmittedAt          *time.Time
	HasFoodOrders        bool
	FoodOrderID          string
	OrderPageURL         string
	FoodChoices          map[string]string
	FoodChoicesJSON      string
	CalculatedAmount     float64
	CoverFees            bool
	PayPalOrderID        string
	PayPalOrderCreatedAt *time.Time // ADD THIS LINE
	PayPalStatus         string
	PayPalDetails        string // ADD THIS LINE
}

type FundraiserSubmission struct {
	FormID               string
	AccessToken          string
	SubmissionDate       time.Time
	FullName             string
	FirstName            string
	LastName             string
	Email                string
	School               string
	Describe             string
	DonorStatus          string
	StudentCount         int
	Students             []Student
	DonationItems        []StudentDonation
	TotalAmount          float64
	CoverFees            bool
	CalculatedAmount     float64
	PayPalOrderID        string
	PayPalOrderCreatedAt *time.Time
	PayPalStatus         string
	PayPalDetails        string
	Submitted            bool
	SubmittedAt          *time.Time

	// Email tracking fields
	ConfirmationEmailSent   bool
	ConfirmationEmailSentAt *time.Time
	AdminNotificationSent   bool
	AdminNotificationSentAt *time.Time
}

type StudentDonation struct {
	StudentName string  `json:"student_name"`
	Amount      float64 `json:"amount"`
}

// =============================================================================
// DATABASE CONNECTION AND SETUP
// =============================================================================

// InitDB initializes the database with connection pooling and resilience
func InitDB(dataSourceName string) error {
	var initErr error

	dbMu.Lock()
	defer dbMu.Unlock()

	// Close existing connection if any
	if db != nil {
		db.Close()
	}

	// Initialize new connection with retry logic
	initErr = initDBWithRetry(dataSourceName, 3)
	return initErr
}

func initDBWithRetry(dataSourceName string, maxRetries int) error {
	var err error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		db, err = sql.Open("sqlite", dataSourceName)
		if err != nil {
			logger.LogWarn("Database connection attempt %d failed: %v", attempt, err)
			if attempt < maxRetries {
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			return fmt.Errorf("failed to open database after %d attempts: %w", maxRetries, err)
		}

		// Configure connection pool
		db.SetMaxOpenConns(maxOpenConns)
		db.SetMaxIdleConns(maxIdleConns)
		db.SetConnMaxLifetime(connMaxLifetime)
		db.SetConnMaxIdleTime(connMaxIdleTime)

		// Test the connection
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		err = db.PingContext(ctx)
		cancel()

		if err != nil {
			logger.LogWarn("Database ping attempt %d failed: %v", attempt, err)
			db.Close()
			if attempt < maxRetries {
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			return fmt.Errorf("failed to ping database after %d attempts: %w", maxRetries, err)
		}

		// Enable optimizations with error handling
		if err := enablePragmasWithRetry(db); err != nil {
			logger.LogWarn("Failed to enable some database optimizations: %v", err)
			// Don't fail initialization for pragma errors
		}

		logger.LogInfo("Database connection established successfully (attempt %d)", attempt)
		return nil
	}

	return fmt.Errorf("failed to initialize database after %d attempts", maxRetries)
}

func enablePragmasWithRetry(conn *sql.DB) error {
	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA cache_size = -64000",
		"PRAGMA temp_store = MEMORY",
		"PRAGMA mmap_size = 268435456",
	}

	var lastErr error
	for _, pragma := range pragmas {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
		_, err := conn.ExecContext(ctx, pragma)
		cancel()

		if err != nil {
			logger.LogWarn("Failed to execute %s: %v", pragma, err)
			lastErr = err
		}
	}
	return lastErr
}

// GetDB returns the database connection with health check
func GetDB() (*sql.DB, error) {
	dbMu.RLock()
	defer dbMu.RUnlock()

	if db == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	// Quick health check
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		logger.LogError("Database health check failed: %v", err)
		return nil, fmt.Errorf("database connection unhealthy: %w", err)
	}

	return db, nil
}

// CloseDB closes the database connection gracefully
func CloseDB() error {
	dbMu.Lock()
	defer dbMu.Unlock()

	if db != nil {
		err := db.Close()
		db = nil
		return err
	}
	return nil
}

// =============================================================================
// SCHEMA DEFINITIONS
// =============================================================================

const membershipTableSchema = `
    CREATE TABLE IF NOT EXISTS membership_submissions (
        form_id TEXT PRIMARY KEY,
        access_token TEXT,
        submission_date TEXT NOT NULL,
        full_name TEXT NOT NULL,
        first_name TEXT,
        last_name TEXT,
        email TEXT NOT NULL,
        school TEXT,
        membership TEXT,
        membership_status TEXT,
        describe TEXT,
        student_count INTEGER DEFAULT 0,
        students_json TEXT DEFAULT '[]',
        interests_json TEXT DEFAULT '[]',
        addons_json TEXT DEFAULT '[]',
        fees_json TEXT DEFAULT '{}',
        donation REAL DEFAULT 0,
        calculated_amount REAL DEFAULT 0,
        cover_fees BOOLEAN DEFAULT 0,
        paypal_order_id TEXT,
        paypal_order_created_at TEXT,
        paypal_status TEXT,
        paypal_details TEXT,
        paypal_webhook TEXT,
        submitted BOOLEAN DEFAULT 0,
        submitted_at TEXT,
        confirmation_email_sent BOOLEAN DEFAULT 0,
        confirmation_email_sent_at TEXT,
        admin_notification_sent BOOLEAN DEFAULT 0,
        admin_notification_sent_at TEXT
    );
    CREATE INDEX IF NOT EXISTS idx_membership_submission_date ON membership_submissions(submission_date);
    CREATE INDEX IF NOT EXISTS idx_membership_email ON membership_submissions(email);
    CREATE INDEX IF NOT EXISTS idx_membership_submitted ON membership_submissions(submitted);`

const eventTableSchema = `
    CREATE TABLE IF NOT EXISTS event_submissions (
        form_id TEXT PRIMARY KEY,
        access_token TEXT,
        submission_date TEXT NOT NULL,
        event TEXT NOT NULL,
        full_name TEXT NOT NULL,
        first_name TEXT,
        last_name TEXT,
        email TEXT NOT NULL,
        school TEXT,
        student_count INTEGER DEFAULT 0,
        students_json TEXT DEFAULT '[]',
        submitted BOOLEAN DEFAULT 0,
        submitted_at TEXT,
        food_choices_json TEXT DEFAULT '{}',
        food_order_id TEXT DEFAULT '',
        order_page_url TEXT DEFAULT '',
        calculated_amount REAL DEFAULT 0,
        cover_fees BOOLEAN DEFAULT 0,
        paypal_order_id TEXT,
        paypal_status TEXT
    );
    CREATE INDEX IF NOT EXISTS idx_event_submission_date ON event_submissions(submission_date);
    CREATE INDEX IF NOT EXISTS idx_event_email ON event_submissions(email);`

const fundraiserTableSchema = `
	CREATE TABLE IF NOT EXISTS fundraiser_submissions (
		form_id TEXT PRIMARY KEY,
		access_token TEXT,
		submission_date TEXT NOT NULL,
		full_name TEXT NOT NULL,
		first_name TEXT,
		last_name TEXT,
		email TEXT NOT NULL,
		school TEXT,
		describe TEXT,
		donor_status TEXT,
		student_count INTEGER DEFAULT 0,
		students_json TEXT DEFAULT '[]',
		donation_items_json TEXT DEFAULT '[]',
		total_amount REAL DEFAULT 0,
		cover_fees BOOLEAN DEFAULT 0,
		calculated_amount REAL DEFAULT 0,
		paypal_order_id TEXT,
		paypal_order_created_at TEXT,
		paypal_status TEXT,
		paypal_details TEXT,
		submitted BOOLEAN DEFAULT 0,
		submitted_at TEXT,
		confirmation_email_sent BOOLEAN DEFAULT 0,
		confirmation_email_sent_at TEXT,
		admin_notification_sent BOOLEAN DEFAULT 0,
		admin_notification_sent_at TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_fundraiser_submission_date ON fundraiser_submissions(submission_date);
	CREATE INDEX IF NOT EXISTS idx_fundraiser_email ON fundraiser_submissions(email);
	CREATE INDEX IF NOT EXISTS idx_fundraiser_submitted ON fundraiser_submissions(submitted);`

// =============================================================================
// TABLE CREATION AND MIGRATIONS
// =============================================================================

func CreateTables() error {
	tables := []struct {
		name string
		fn   func() error
	}{
		{"membership", createMembershipTable},
		{"event", createEventTable},
		{"fundraiser", createFundraiserTable},
	}

	for _, table := range tables {
		if err := table.fn(); err != nil {
			return fmt.Errorf("failed to create %s table: %w", table.name, err)
		}
	}

	// Run migrations
	if err := migrateEventTable(); err != nil {
		return fmt.Errorf("failed to migrate event table: %w", err)
	}

	return nil
}

func createMembershipTable() error {
	_, err := db.Exec(membershipTableSchema)
	return err
}

func createEventTable() error {
	_, err := db.Exec(eventTableSchema)
	return err
}

func migrateEventTable() error {
	// First, check if we need to migrate from old schema to new schema
	var oldColumnCount int
	err := db.QueryRow(`
		SELECT COUNT(*) FROM pragma_table_info('event_submissions') 
		WHERE name IN ('student_meal_provided', 'additional_meal', 'festival_lunch', 'show_food_options')
	`).Scan(&oldColumnCount)

	if err != nil {
		return fmt.Errorf("failed to check for old columns: %w", err)
	}

	// If old columns exist, we need to migrate the table
	if oldColumnCount > 0 {
		logger.LogInfo("Migrating event_submissions table from old schema to new schema")

		// Create new table with updated schema
		createNewTableSQL := `
			CREATE TABLE event_submissions_new (
				form_id TEXT PRIMARY KEY,
				access_token TEXT,
				submission_date TEXT NOT NULL,
				event TEXT NOT NULL,
				full_name TEXT NOT NULL,
				first_name TEXT,
				last_name TEXT,
				email TEXT NOT NULL,
				school TEXT,
				student_count INTEGER DEFAULT 0,
				students_json TEXT DEFAULT '[]',
				submitted BOOLEAN DEFAULT 0,
				submitted_at TEXT,
				food_choices_json TEXT DEFAULT '{}',
				food_order_id TEXT DEFAULT '',
				order_page_url TEXT DEFAULT '',
				calculated_amount REAL DEFAULT 0,
				cover_fees BOOLEAN DEFAULT 0,
				paypal_order_id TEXT,
				paypal_status TEXT
			)`

		_, err = db.Exec(createNewTableSQL)
		if err != nil {
			return fmt.Errorf("failed to create new event_submissions table: %w", err)
		}

		// Copy data from old table to new table, converting old food selections to JSON
		copyDataSQL := `
			INSERT INTO event_submissions_new (
				form_id, access_token, submission_date, event, full_name, first_name, last_name, 
				email, school, student_count, students_json, submitted, submitted_at, 
				food_choices_json, food_order_id, order_page_url, calculated_amount, cover_fees, 
				paypal_order_id, paypal_status
			)
			SELECT 
				form_id, access_token, submission_date, event, full_name, first_name, last_name,
				email, school, student_count, students_json, submitted, submitted_at,
				CASE 
					WHEN student_meal_provided > 0 OR additional_meal > 0 OR festival_lunch > 0 THEN
						'{"legacy_data":{"student_meal_provided":' || COALESCE(student_meal_provided, 0) || 
						',"additional_meal":' || COALESCE(additional_meal, 0) || 
						',"festival_lunch":' || COALESCE(festival_lunch, 0) || '}}'
					ELSE '{}'
				END as food_choices_json,
				COALESCE(food_order_id, '') as food_order_id,
				COALESCE(order_page_url, '') as order_page_url,
				COALESCE(calculated_amount, 0) as calculated_amount,
				COALESCE(cover_fees, 0) as cover_fees,
				paypal_order_id, paypal_status
			FROM event_submissions`

		_, err = db.Exec(copyDataSQL)
		if err != nil {
			return fmt.Errorf("failed to copy data to new table: %w", err)
		}

		// Drop old table and rename new table
		_, err = db.Exec(`DROP TABLE event_submissions`)
		if err != nil {
			return fmt.Errorf("failed to drop old table: %w", err)
		}

		_, err = db.Exec(`ALTER TABLE event_submissions_new RENAME TO event_submissions`)
		if err != nil {
			return fmt.Errorf("failed to rename new table: %w", err)
		}

		// Recreate indexes
		_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_event_submission_date ON event_submissions(submission_date)`)
		if err != nil {
			return fmt.Errorf("failed to create submission_date index: %w", err)
		}

		_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_event_email ON event_submissions(email)`)
		if err != nil {
			return fmt.Errorf("failed to create email index: %w", err)
		}

		logger.LogInfo("Successfully migrated event_submissions table to new schema")
	} else {
		// Check if order_page_url column exists (for newer installations)
		var count int
		err := db.QueryRow(`
			SELECT COUNT(*) FROM pragma_table_info('event_submissions') 
			WHERE name='order_page_url'
		`).Scan(&count)

		if err != nil {
			return fmt.Errorf("failed to check for order_page_url column: %w", err)
		}

		// If column doesn't exist, add it
		if count == 0 {
			_, err = db.Exec(`ALTER TABLE event_submissions ADD COLUMN order_page_url TEXT DEFAULT ''`)
			if err != nil {
				return fmt.Errorf("failed to add order_page_url column: %w", err)
			}
			logger.LogInfo("Added order_page_url column to event_submissions table")
		}
	}

	return nil
}

func createFundraiserTable() error {
	_, err := db.Exec(fundraiserTableSchema)
	return err
}

// =============================================================================
// UTILITY FUNCTIONS (JSON AND TIME HANDLING)
// =============================================================================

// JSON utilities

func marshalJSON(v interface{}) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("failed to marshal JSON: %w", err)
	}
	return string(data), nil
}

func unmarshalJSON(data string, v interface{}) error {
	if err := json.Unmarshal([]byte(data), v); err != nil {
		return fmt.Errorf("failed to unmarshal JSON: %w", err)
	}
	return nil
}

func unmarshalNullableJSON(nullStr sql.NullString, v interface{}) error {
	if !nullStr.Valid || nullStr.String == "" {
		// Handle different types with appropriate defaults
		switch ptr := v.(type) {
		case *[]Student:
			*ptr = []Student{}
		case *[]string:
			*ptr = []string{}
		case *map[string]int:
			*ptr = make(map[string]int)
		default:
			// For other types, try to unmarshal empty JSON
			if err := json.Unmarshal([]byte("{}"), v); err != nil {
				// If that fails, try empty array
				if err := json.Unmarshal([]byte("[]"), v); err != nil {
					return fmt.Errorf("failed to set default for type %T: %w", v, err)
				}
			}
		}
		return nil
	}

	jsonData := nullStr.String

	// Special handling for fees field - convert old array format to new map format
	if ptr, ok := v.(*map[string]int); ok {
		// First try to unmarshal as map (new format)
		var feeMap map[string]int
		if err := json.Unmarshal([]byte(jsonData), &feeMap); err == nil {
			*ptr = feeMap
			return nil
		}

		// If that fails, try to unmarshal as array (old format) and convert
		var feeArray []string
		if err := json.Unmarshal([]byte(jsonData), &feeArray); err == nil {
			// Convert array to map with count of 1 for each item
			convertedMap := make(map[string]int)
			for _, fee := range feeArray {
				if fee != "" {
					convertedMap[fee] = 1
				}
			}
			*ptr = convertedMap
			logger.LogInfo("Converted legacy fees array to map format")
			return nil
		}

		// If both fail, set empty map
		*ptr = make(map[string]int)
		logger.LogWarn("Could not parse fees JSON, using empty map: %s", jsonData)
		return nil
	}

	// For all other types, use standard unmarshaling
	if err := json.Unmarshal([]byte(jsonData), v); err != nil {
		return fmt.Errorf("failed to unmarshal JSON: %w", err)
	}
	return nil
}

// Time utilities

func formatTime(t time.Time) string {
	return t.Format(TimeFormat)
}

func formatNullableTime(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return t.Format(TimeFormat)
}

func parseTime(timeStr string) (time.Time, error) {
	return time.Parse(TimeFormat, timeStr)
}

func parseNullableTime(nullStr sql.NullString) (*time.Time, error) {
	if !nullStr.Valid || nullStr.String == "" {
		return nil, nil
	}

	parsedTime, err := time.Parse(TimeFormat, nullStr.String)
	if err != nil {
		return nil, fmt.Errorf("failed to parse time: %w", err)
	}

	return &parsedTime, nil
}

// =============================================================================
// GENERIC DATABASE OPERATIONS
// =============================================================================

// ExecDB executes a query with better error handling and timeouts
func ExecDB(query string, args ...interface{}) (sql.Result, error) {
	dbConn, err := GetDB()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	result, err := dbConn.ExecContext(ctx, query, args...)
	if err != nil {
		logger.LogError("Database exec failed: query=%s, error=%v", query, err)
		return nil, fmt.Errorf("database execution failed: %w", err)
	}

	return result, nil
}

// QueryDB executes a query with timeout and returns rows
func QueryDB(query string, args ...interface{}) (*sql.Rows, error) {
	dbConn, err := GetDB()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	rows, err := dbConn.QueryContext(ctx, query, args...)
	if err != nil {
		logger.LogError("Database query failed: query=%s, error=%v", query, err)
		return nil, fmt.Errorf("database query failed: %w", err)
	}

	return rows, nil
}

// QueryRowDB executes a query that returns a single row
func QueryRowDB(query string, args ...interface{}) *sql.Row {
	dbConn, _ := GetDB() // We'll let the query fail if DB is unavailable

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	return dbConn.QueryRowContext(ctx, query, args...)
}
