package data

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// =============================================================================
// EVENT REPOSITORY
// =============================================================================

// Repository struct and constructor
type EventRepository struct {
	db *sql.DB
}

func NewEventRepository() *EventRepository {
	return &EventRepository{db: db}
}

// =============================================================================
// CORE CRUD OPERATIONS
// =============================================================================

func (r *EventRepository) Insert(sub EventSubmission) error {
	studentsJSON, err := marshalJSON(sub.Students)
	if err != nil {
		return fmt.Errorf("failed to marshal students: %w", err)
	}

	const stmt = `
		INSERT INTO event_submissions (
			form_id, access_token, submission_date, event, full_name, first_name, last_name, email, school,
			student_count, students_json, submitted, submitted_at, food_choices_json, food_order_id, 
			order_page_url, calculated_amount, cover_fees, paypal_order_id, paypal_status
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err = ExecDB(stmt,
		sub.FormID, sub.AccessToken, formatTime(sub.SubmissionDate), sub.Event,
		sub.FullName, sub.FirstName, sub.LastName, sub.Email, sub.School,
		sub.StudentCount, studentsJSON, sub.Submitted,
		formatNullableTime(sub.SubmittedAt),
		sub.FoodChoicesJSON, sub.FoodOrderID, sub.OrderPageURL,
		sub.CalculatedAmount, sub.CoverFees, sub.PayPalOrderID, sub.PayPalStatus,
	)

	if err != nil {
		return fmt.Errorf("failed to insert event submission: %w", err)
	}

	return nil
}

func (r *EventRepository) GetByID(formID string) (*EventSubmission, error) {
	const stmt = `
		SELECT form_id, access_token, submission_date, event, full_name, first_name, last_name, email, school,
			student_count, students_json, submitted, submitted_at, has_food_orders, food_choices_json, food_order_id, 
			order_page_url, calculated_amount, cover_fees, paypal_order_id, paypal_order_created_at, 
			paypal_status, paypal_details
		FROM event_submissions WHERE form_id = ?`

	row := QueryRowDB(stmt, formID)
	return r.scanEventRow(row)
}
func (r *EventRepository) GetByYear(year int) ([]EventSubmission, error) {
	start := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(1, 0, 0)

	const stmt = `
		SELECT form_id, access_token, submission_date, event, full_name, first_name, last_name, email, school,
			student_count, students_json, submitted, submitted_at, has_food_orders, food_choices_json, food_order_id, 
			order_page_url, calculated_amount, cover_fees, paypal_order_id, paypal_order_created_at, 
			paypal_status, paypal_details
		FROM event_submissions
		WHERE submission_date >= ? AND submission_date < ? AND submitted = 1
		ORDER BY submission_date`

	rows, err := QueryDB(stmt, formatTime(start), formatTime(end))
	if err != nil {
		return nil, fmt.Errorf("failed to query events by year: %w", err)
	}
	defer rows.Close()

	var result []EventSubmission
	for rows.Next() {
		event, err := r.scanEventRows(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan event rows: %w", err)
		}
		result = append(result, *event)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating event rows: %w", err)
	}

	return result, nil
}

// =============================================================================
// SCANNING AND POPULATION HELPERS
// =============================================================================

func (r *EventRepository) scanEventRow(row *sql.Row) (*EventSubmission, error) {
	var sub EventSubmission
	var submissionDate, submittedAt sql.NullString
	var studentsJSON, foodChoicesJSON, foodOrderID, orderPageURL sql.NullString
	var calculatedAmount sql.NullFloat64
	var coverFees, hasFoodOrders sql.NullBool
	var paypalOrderID, paypalOrderCreatedAt, paypalStatus, paypalDetails sql.NullString

	err := row.Scan(
		&sub.FormID, &sub.AccessToken, &submissionDate, &sub.Event, &sub.FullName, &sub.FirstName,
		&sub.LastName, &sub.Email, &sub.School, &sub.StudentCount, &studentsJSON,
		&sub.Submitted, &submittedAt, &hasFoodOrders, &foodChoicesJSON, &foodOrderID, &orderPageURL,
		&calculatedAmount, &coverFees, &paypalOrderID, &paypalOrderCreatedAt, &paypalStatus, &paypalDetails,
	)
	if err != nil {
		return nil, err
	}

	// Handle nullable fields
	if foodChoicesJSON.Valid && foodChoicesJSON.String != "" {
		sub.FoodChoicesJSON = foodChoicesJSON.String
		_ = json.Unmarshal([]byte(foodChoicesJSON.String), &sub.FoodChoices)
	} else {
		sub.FoodChoices = make(map[string]string)
	}

	if foodOrderID.Valid {
		sub.FoodOrderID = foodOrderID.String
	}

	if orderPageURL.Valid {
		sub.OrderPageURL = orderPageURL.String
	}

	if calculatedAmount.Valid {
		sub.CalculatedAmount = calculatedAmount.Float64
	}

	if coverFees.Valid {
		sub.CoverFees = coverFees.Bool
	}

	if hasFoodOrders.Valid {
		sub.HasFoodOrders = hasFoodOrders.Bool
	}

	if paypalOrderID.Valid {
		sub.PayPalOrderID = paypalOrderID.String
	}

	if paypalOrderCreatedAt.Valid {
		if parsedTime, err := parseTime(paypalOrderCreatedAt.String); err == nil {
			sub.PayPalOrderCreatedAt = &parsedTime
		}
	}

	if paypalStatus.Valid {
		sub.PayPalStatus = paypalStatus.String
	}

	if paypalDetails.Valid {
		sub.PayPalDetails = paypalDetails.String
	}

	// Parse dates and other fields
	if err := r.populateEventFromJSON(&sub, submissionDate, submittedAt, studentsJSON.String); err != nil {
		return nil, err
	}

	return &sub, nil
}

// Add scanEventRows for multiple rows
func (r *EventRepository) scanEventRows(rows *sql.Rows) (*EventSubmission, error) {
	var sub EventSubmission
	var submissionDate, submittedAt sql.NullString
	var studentsJSON, foodChoicesJSON, foodOrderID, orderPageURL sql.NullString
	var calculatedAmount sql.NullFloat64
	var coverFees, hasFoodOrders sql.NullBool
	var paypalOrderID, paypalOrderCreatedAt, paypalStatus, paypalDetails sql.NullString

	err := rows.Scan(
		&sub.FormID, &sub.AccessToken, &submissionDate, &sub.Event, &sub.FullName, &sub.FirstName,
		&sub.LastName, &sub.Email, &sub.School, &sub.StudentCount, &studentsJSON,
		&sub.Submitted, &submittedAt, &hasFoodOrders, &foodChoicesJSON, &foodOrderID, &orderPageURL,
		&calculatedAmount, &coverFees, &paypalOrderID, &paypalOrderCreatedAt, &paypalStatus, &paypalDetails,
	)
	if err != nil {
		return nil, err
	}

	// Handle nullable fields (same as scanEventRow)
	if foodChoicesJSON.Valid && foodChoicesJSON.String != "" {
		sub.FoodChoicesJSON = foodChoicesJSON.String
		_ = json.Unmarshal([]byte(foodChoicesJSON.String), &sub.FoodChoices)
	} else {
		sub.FoodChoices = make(map[string]string)
	}

	if foodOrderID.Valid {
		sub.FoodOrderID = foodOrderID.String
	}

	if orderPageURL.Valid {
		sub.OrderPageURL = orderPageURL.String
	}

	if calculatedAmount.Valid {
		sub.CalculatedAmount = calculatedAmount.Float64
	}

	if coverFees.Valid {
		sub.CoverFees = coverFees.Bool
	}

	if hasFoodOrders.Valid {
		sub.HasFoodOrders = hasFoodOrders.Bool
	}

	if paypalOrderID.Valid {
		sub.PayPalOrderID = paypalOrderID.String
	}

	if paypalOrderCreatedAt.Valid {
		if parsedTime, err := parseTime(paypalOrderCreatedAt.String); err == nil {
			sub.PayPalOrderCreatedAt = &parsedTime
		}
	}

	if paypalStatus.Valid {
		sub.PayPalStatus = paypalStatus.String
	}

	if paypalDetails.Valid {
		sub.PayPalDetails = paypalDetails.String
	}

	// Parse dates and other fields
	if err := r.populateEventFromJSON(&sub, submissionDate, submittedAt, studentsJSON.String); err != nil {
		return nil, err
	}

	return &sub, nil
}

func (r *EventRepository) populateEventFromJSON(sub *EventSubmission,
	submissionDate, submittedAt sql.NullString, studentsJSON string) error {

	// Parse submission date
	if submissionDate.Valid {
		parsedTime, err := parseTime(submissionDate.String)
		if err != nil {
			return fmt.Errorf("failed to parse submission date: %w", err)
		}
		sub.SubmissionDate = parsedTime
	}

	// Parse submitted at
	submittedAtTime, err := parseNullableTime(submittedAt)
	if err != nil {
		return fmt.Errorf("failed to parse submitted at: %w", err)
	}
	sub.SubmittedAt = submittedAtTime

	// Unmarshal students
	if err := unmarshalJSON(studentsJSON, &sub.Students); err != nil {
		return fmt.Errorf("failed to unmarshal students: %w", err)
	}

	return nil
}

// =============================================================================
// UPDATE OPERATIONS
// =============================================================================

// Payment updates

func (r *EventRepository) UpdatePayment(sub EventSubmission) error {
	const stmt = `
		UPDATE event_submissions 
		SET food_choices_json = ?, has_food_orders=?, food_order_id=?, calculated_amount = ?, cover_fees = ?
		WHERE form_id = ?`

	_, err := ExecDB(stmt,
		sub.FoodChoicesJSON, sub.HasFoodOrders, sub.FoodOrderID, sub.CalculatedAmount, sub.CoverFees, sub.FormID,
	)

	if err != nil {
		return fmt.Errorf("failed to update event payment: %w", err)
	}

	return nil
}

func (r *EventRepository) UpdateOrderPageURL(formID, orderPageURL string) error {
	const stmt = `UPDATE event_submissions SET order_page_url = ? WHERE form_id = ?`

	_, err := ExecDB(stmt, orderPageURL, formID)
	if err != nil {
		return fmt.Errorf("failed to update order page URL: %w", err)
	}

	return nil
}

// =============================================================================
// LEGACY BACKWARD COMPATIBILITY FUNCTIONS
// =============================================================================

func InsertEvent(sub EventSubmission) error {
	repo := NewEventRepository()
	return repo.Insert(sub)
}

func GetEventByID(formID string) (*EventSubmission, error) {
	repo := NewEventRepository()
	return repo.GetByID(formID)
}

func GetEventsByYear(year int) ([]EventSubmission, error) {
	repo := NewEventRepository()
	return repo.GetByYear(year)
}

func UpdateEventPayment(sub EventSubmission) error {
	repo := NewEventRepository()
	return repo.UpdatePayment(sub)
}

func UpdateEventPayPalOrder(formID, orderID string, createdAt *time.Time) error {
	const stmt = `UPDATE event_submissions SET paypal_order_id = ?, paypal_order_created_at = ? WHERE form_id = ?`
	_, err := ExecDB(stmt, orderID, formatNullableTime(createdAt), formID)
	if err != nil {
		return fmt.Errorf("failed to update PayPal order: %w", err)
	}
	return nil
}

func UpdateEventPayPalCapture(formID, paypalDetails, status string, submittedAt *time.Time) error {
	const stmt = `
        UPDATE event_submissions
        SET paypal_details = ?, paypal_status = ?, submitted = 1, submitted_at = ?
        WHERE form_id = ?`
	_, err := ExecDB(stmt, paypalDetails, status, formatNullableTime(submittedAt), formID)
	if err != nil {
		return fmt.Errorf("failed to update PayPal capture: %w", err)
	}
	return nil
}

func UpdateEventOrderPageURL(formID, orderPageURL string) error {
	repo := NewEventRepository()
	return repo.UpdateOrderPageURL(formID, orderPageURL)
}
