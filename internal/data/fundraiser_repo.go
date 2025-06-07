package data

import (
	"database/sql"
	"fmt"
	"time"
)

// =============================================================================
// FUNDRAISER REPOSITORY
// =============================================================================

// Repository struct and constructor

type FundraiserRepository struct {
	db *sql.DB
}

func NewFundraiserRepository() *FundraiserRepository {
	return &FundraiserRepository{db: db}
}

// =============================================================================
// CORE CRUD OPERATIONS
// =============================================================================

func (r *FundraiserRepository) Insert(sub FundraiserSubmission) error {
	studentsJSON, err := marshalJSON(sub.Students)
	if err != nil {
		return fmt.Errorf("failed to marshal students: %w", err)
	}

	donationItemsJSON, err := marshalJSON(sub.DonationItems)
	if err != nil {
		return fmt.Errorf("failed to marshal donation items: %w", err)
	}

	const stmt = `
		INSERT INTO fundraiser_submissions (
			form_id, access_token, submission_date, full_name, first_name, last_name, email, school,
			describe, donor_status, student_count, students_json, donation_items_json, total_amount,
			cover_fees, calculated_amount, paypal_order_id, paypal_order_created_at, paypal_status,
			paypal_details, submitted, submitted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err = ExecDB(stmt,
		sub.FormID, sub.AccessToken, formatTime(sub.SubmissionDate),
		sub.FullName, sub.FirstName, sub.LastName, sub.Email, sub.School,
		sub.Describe, sub.DonorStatus, sub.StudentCount, studentsJSON,
		donationItemsJSON, sub.TotalAmount, sub.CoverFees, sub.CalculatedAmount,
		sub.PayPalOrderID, formatNullableTime(sub.PayPalOrderCreatedAt),
		sub.PayPalStatus, sub.PayPalDetails, sub.Submitted,
		formatNullableTime(sub.SubmittedAt),
	)

	if err != nil {
		return fmt.Errorf("failed to insert fundraiser submission: %w", err)
	}

	return nil
}

func (r *FundraiserRepository) GetByID(formID string) (*FundraiserSubmission, error) {
	const stmt = `
		SELECT form_id, access_token, submission_date, full_name, first_name, last_name, email, school,
			describe, donor_status, student_count, students_json, donation_items_json, total_amount,
			cover_fees, calculated_amount, paypal_order_id, paypal_order_created_at, paypal_status,
			paypal_details, submitted, submitted_at
		FROM fundraiser_submissions WHERE form_id = ?`

	row := QueryRowDB(stmt, formID)
	return r.scanFundraiserRow(row)
}
func (r *FundraiserRepository) GetByYear(year int) ([]FundraiserSubmission, error) {
	start := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(1, 0, 0)

	const stmt = `
		SELECT form_id, access_token, submission_date, full_name, first_name, last_name, email, school,
			describe, donor_status, student_count, students_json, donation_items_json, total_amount,
			cover_fees, calculated_amount, paypal_order_id, paypal_order_created_at, paypal_status,
			paypal_details, submitted, submitted_at
		FROM fundraiser_submissions
		WHERE submission_date >= ? AND submission_date < ?
		ORDER BY submission_date`

	rows, err := QueryDB(stmt, formatTime(start), formatTime(end))
	if err != nil {
		return nil, fmt.Errorf("failed to query fundraisers by year: %w", err)
	}
	defer rows.Close()

	var result []FundraiserSubmission
	for rows.Next() {
		fundraiser, err := r.scanFundraiserRows(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan fundraiser rows: %w", err)
		}
		result = append(result, *fundraiser)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating fundraiser rows: %w", err)
	}

	return result, nil
}

// =============================================================================
// SCANNING AND POPULATION HELPERS
// =============================================================================

func (r *FundraiserRepository) scanFundraiserRow(row *sql.Row) (*FundraiserSubmission, error) {
	var sub FundraiserSubmission
	var submissionDate, paypalOrderCreatedAt, submittedAt sql.NullString
	var studentsJSON, donationItemsJSON sql.NullString

	err := row.Scan(
		&sub.FormID, &sub.AccessToken, &submissionDate, &sub.FullName, &sub.FirstName, &sub.LastName,
		&sub.Email, &sub.School, &sub.Describe, &sub.DonorStatus, &sub.StudentCount,
		&studentsJSON, &donationItemsJSON, &sub.TotalAmount, &sub.CoverFees, &sub.CalculatedAmount,
		&sub.PayPalOrderID, &paypalOrderCreatedAt, &sub.PayPalStatus, &sub.PayPalDetails,
		&sub.Submitted, &submittedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to scan fundraiser: %w", err)
	}

	if err := r.populateFundraiserFromJSON(&sub, submissionDate, paypalOrderCreatedAt, submittedAt,
		studentsJSON, donationItemsJSON); err != nil {
		return nil, fmt.Errorf("failed to populate fundraiser from JSON: %w", err)
	}

	return &sub, nil
}
func (r *FundraiserRepository) scanFundraiserRows(rows *sql.Rows) (*FundraiserSubmission, error) {
	var sub FundraiserSubmission
	var submissionDate, paypalOrderCreatedAt, submittedAt sql.NullString
	var studentsJSON, donationItemsJSON sql.NullString

	err := rows.Scan(
		&sub.FormID, &sub.AccessToken, &submissionDate, &sub.FullName, &sub.FirstName, &sub.LastName,
		&sub.Email, &sub.School, &sub.Describe, &sub.DonorStatus, &sub.StudentCount,
		&studentsJSON, &donationItemsJSON, &sub.TotalAmount, &sub.CoverFees, &sub.CalculatedAmount,
		&sub.PayPalOrderID, &paypalOrderCreatedAt, &sub.PayPalStatus, &sub.PayPalDetails,
		&sub.Submitted, &submittedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to scan fundraiser: %w", err)
	}

	if err := r.populateFundraiserFromJSON(&sub, submissionDate, paypalOrderCreatedAt, submittedAt,
		studentsJSON, donationItemsJSON); err != nil {
		return nil, fmt.Errorf("failed to populate fundraiser from JSON: %w", err)
	}

	return &sub, nil
}
func (r *FundraiserRepository) populateFundraiserFromJSON(sub *FundraiserSubmission,
	submissionDate, paypalOrderCreatedAt, submittedAt sql.NullString,
	studentsJSON, donationItemsJSON sql.NullString) error {

	// Parse dates
	if submissionDate.Valid {
		parsedTime, err := parseTime(submissionDate.String)
		if err != nil {
			return fmt.Errorf("failed to parse submission date: %w", err)
		}
		sub.SubmissionDate = parsedTime
	}

	paypalCreatedAt, err := parseNullableTime(paypalOrderCreatedAt)
	if err != nil {
		return fmt.Errorf("failed to parse paypal created at: %w", err)
	}
	sub.PayPalOrderCreatedAt = paypalCreatedAt

	submittedAtTime, err := parseNullableTime(submittedAt)
	if err != nil {
		return fmt.Errorf("failed to parse submitted at: %w", err)
	}
	sub.SubmittedAt = submittedAtTime

	// Unmarshal JSON fields
	if err := unmarshalNullableJSON(studentsJSON, &sub.Students); err != nil {
		return fmt.Errorf("failed to unmarshal students: %w", err)
	}

	if err := unmarshalNullableJSON(donationItemsJSON, &sub.DonationItems); err != nil {
		return fmt.Errorf("failed to unmarshal donation items: %w", err)
	}

	return nil
}

// =============================================================================
// UPDATE OPERATIONS
// =============================================================================

// PayPal updates

func (r *FundraiserRepository) UpdatePayPalOrder(formID, orderID string, createdAt *time.Time) error {
	const stmt = `UPDATE fundraiser_submissions SET paypal_order_id = ?, paypal_order_created_at = ? WHERE form_id = ?`

	_, err := ExecDB(stmt, orderID, formatNullableTime(createdAt), formID)
	if err != nil {
		return fmt.Errorf("failed to update PayPal order: %w", err)
	}

	return nil
}

func (r *FundraiserRepository) UpdatePayPalCapture(formID, paypalDetails, status string, submittedAt *time.Time) error {
	const stmt = `
		UPDATE fundraiser_submissions
		SET paypal_details = ?, paypal_status = ?, submitted = 1, submitted_at = ?
		WHERE form_id = ?`

	_, err := ExecDB(stmt, paypalDetails, status, formatNullableTime(submittedAt), formID)
	if err != nil {
		return fmt.Errorf("failed to update PayPal capture: %w", err)
	}

	return nil
}

// Payment updates

func (r *FundraiserRepository) UpdatePayment(sub FundraiserSubmission) error {
	donationItemsJSON, err := marshalJSON(sub.DonationItems)
	if err != nil {
		return fmt.Errorf("failed to marshal donation items: %w", err)
	}

	const stmt = `
		UPDATE fundraiser_submissions 
		SET donation_items_json = ?, total_amount = ?, cover_fees = ?, calculated_amount = ?, 
			submitted = ?, submitted_at = ? 
		WHERE form_id = ?`

	_, err = ExecDB(stmt,
		donationItemsJSON, sub.TotalAmount, sub.CoverFees, sub.CalculatedAmount,
		sub.Submitted, formatNullableTime(sub.SubmittedAt), sub.FormID,
	)

	if err != nil {
		return fmt.Errorf("failed to update fundraiser payment: %w", err)
	}

	return nil
}

// Email updates

func (r *FundraiserRepository) UpdateEmailStatus(formID string, confirmationSent, adminNotificationSent bool) error {
	now := time.Now()
	const stmt = `
        UPDATE fundraiser_submissions 
        SET confirmation_email_sent = ?, confirmation_email_sent_at = ?,
            admin_notification_sent = ?, admin_notification_sent_at = ?
        WHERE form_id = ?`

	_, err := ExecDB(stmt,
		confirmationSent, formatNullableTime(&now),
		adminNotificationSent, formatNullableTime(&now),
		formID)

	if err != nil {
		return fmt.Errorf("failed to update email status: %w", err)
	}

	return nil
}

// =============================================================================
// LEGACY BACKWARD COMPATIBILITY FUNCTIONS
// =============================================================================

func InsertFundraiser(sub FundraiserSubmission) error {
	repo := NewFundraiserRepository()
	return repo.Insert(sub)
}

func GetFundraiserByID(formID string) (*FundraiserSubmission, error) {
	repo := NewFundraiserRepository()
	return repo.GetByID(formID)
}

func GetFundraisersByYear(year int) ([]FundraiserSubmission, error) {
	repo := NewFundraiserRepository()
	return repo.GetByYear(year)
}

func UpdateFundraiserPayPalOrder(formID, orderID string, createdAt *time.Time) error {
	repo := NewFundraiserRepository()
	return repo.UpdatePayPalOrder(formID, orderID, createdAt)
}

func UpdateFundraiserPayPalCapture(formID, paypalDetails, status string, submittedAt *time.Time) error {
	repo := NewFundraiserRepository()
	return repo.UpdatePayPalCapture(formID, paypalDetails, status, submittedAt)
}

func UpdateFundraiserPayment(sub FundraiserSubmission) error {
	repo := NewFundraiserRepository()
	return repo.UpdatePayment(sub)
}

func UpdateFundraiserEmailStatus(formID string, confirmationSent, adminNotificationSent bool) error {
	repo := NewFundraiserRepository()
	return repo.UpdateEmailStatus(formID, confirmationSent, adminNotificationSent)
}
