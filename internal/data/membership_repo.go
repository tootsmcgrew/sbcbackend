package data

import (
	"database/sql"
	"fmt"
	"time"
)

// =============================================================================
// MEMBERSHIP REPOSITORY
// =============================================================================

// Repository struct and constructor

type MembershipRepository struct {
	db *sql.DB
}

func NewMembershipRepository() *MembershipRepository {
	return &MembershipRepository{db: db}
}

// =============================================================================
// CORE CRUD OPERATIONS
// =============================================================================

func (r *MembershipRepository) Insert(sub MembershipSubmission) error {
	studentsJSON, err := marshalJSON(sub.Students)
	if err != nil {
		return fmt.Errorf("failed to marshal students: %w", err)
	}

	addonsJSON, err := marshalJSON(sub.Addons)
	if err != nil {
		return fmt.Errorf("failed to marshal addons: %w", err)
	}

	interestsJSON, err := marshalJSON(sub.Interests)
	if err != nil {
		return fmt.Errorf("failed to marshal interests: %w", err)
	}

	feesJSON, err := marshalJSON(sub.Fees)
	if err != nil {
		return fmt.Errorf("failed to marshal fees: %w", err)
	}

	const stmt = `
		INSERT INTO membership_submissions (
			form_id, access_token, submission_date, full_name, first_name, last_name, email, school,
			membership, membership_status, describe, student_count, students_json, interests_json, 
			addons_json, fees_json, donation, calculated_amount, cover_fees, paypal_order_id, 
			paypal_order_created_at, paypal_status, paypal_details, submitted, submitted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err = ExecDB(stmt,
		sub.FormID, sub.AccessToken, formatTime(sub.SubmissionDate),
		sub.FullName, sub.FirstName, sub.LastName, sub.Email, sub.School,
		sub.Membership, sub.MembershipStatus, sub.Describe, sub.StudentCount,
		studentsJSON, interestsJSON, addonsJSON, feesJSON, sub.Donation,
		sub.CalculatedAmount, sub.CoverFees, sub.PayPalOrderID,
		formatNullableTime(sub.PayPalOrderCreatedAt),
		sub.PayPalStatus, sub.PayPalDetails, sub.Submitted,
		formatNullableTime(sub.SubmittedAt),
	)

	if err != nil {
		return fmt.Errorf("failed to insert membership submission: %w", err)
	}

	return nil
}

func (r *MembershipRepository) GetByID(formID string) (*MembershipSubmission, error) {
	const stmt = `
		SELECT form_id, access_token, submission_date, full_name, first_name, last_name, email, school, 
			membership, membership_status, describe, student_count, students_json, interests_json, 
			addons_json, fees_json, donation, calculated_amount, cover_fees, paypal_order_id, 
			paypal_order_created_at, paypal_status, paypal_details, submitted, submitted_at
		FROM membership_submissions WHERE form_id = ?`

	row := QueryRowDB(stmt, formID)
	return r.scanMembershipRow(row)
}
func (r *MembershipRepository) GetByYear(year int) ([]MembershipSubmission, error) {
	start := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(1, 0, 0)

	const stmt = `
		SELECT form_id, access_token, submission_date, full_name, first_name, last_name, email, school,
			membership, membership_status, describe, student_count, students_json, interests_json,
			addons_json, fees_json, donation, calculated_amount, cover_fees, paypal_order_id, 
			paypal_order_created_at, paypal_status, paypal_details, submitted, submitted_at
		FROM membership_submissions
		WHERE submission_date >= ? AND submission_date < ?
		ORDER BY submission_date`

	rows, err := QueryDB(stmt, formatTime(start), formatTime(end))
	if err != nil {
		return nil, fmt.Errorf("failed to query memberships by year: %w", err)
	}
	defer rows.Close()

	var result []MembershipSubmission
	for rows.Next() {
		membership, err := r.scanMembershipRows(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan membership rows: %w", err)
		}
		result = append(result, *membership)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating membership rows: %w", err)
	}

	return result, nil
}

// =============================================================================
// SCANNING AND POPULATION HELPERS
// =============================================================================

func (r *MembershipRepository) scanMembershipRow(row *sql.Row) (*MembershipSubmission, error) {
	var sub MembershipSubmission
	var submissionDate, paypalOrderCreatedAt, submittedAt sql.NullString
	var studentsJSON, interestsJSON, addonsJSON, feesJSON sql.NullString

	err := row.Scan(
		&sub.FormID, &sub.AccessToken, &submissionDate, &sub.FullName, &sub.FirstName, &sub.LastName,
		&sub.Email, &sub.School, &sub.Membership, &sub.MembershipStatus, &sub.Describe, &sub.StudentCount,
		&studentsJSON, &interestsJSON, &addonsJSON, &feesJSON, &sub.Donation, &sub.CalculatedAmount,
		&sub.CoverFees, &sub.PayPalOrderID, &paypalOrderCreatedAt, &sub.PayPalStatus, &sub.PayPalDetails,
		&sub.Submitted, &submittedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to scan membership: %w", err)
	}

	if err := r.populateMembershipFromJSON(&sub, submissionDate, paypalOrderCreatedAt, submittedAt,
		studentsJSON, interestsJSON, addonsJSON, feesJSON); err != nil {
		return nil, fmt.Errorf("failed to populate membership from JSON: %w", err)
	}

	return &sub, nil
}

func (r *MembershipRepository) scanMembershipRows(rows *sql.Rows) (*MembershipSubmission, error) {
	var sub MembershipSubmission
	var submissionDate, paypalOrderCreatedAt, submittedAt sql.NullString
	var studentsJSON, interestsJSON, addonsJSON, feesJSON sql.NullString

	err := rows.Scan(
		&sub.FormID, &sub.AccessToken, &submissionDate, &sub.FullName, &sub.FirstName, &sub.LastName,
		&sub.Email, &sub.School, &sub.Membership, &sub.MembershipStatus, &sub.Describe, &sub.StudentCount,
		&studentsJSON, &interestsJSON, &addonsJSON, &feesJSON, &sub.Donation, &sub.CalculatedAmount,
		&sub.CoverFees, &sub.PayPalOrderID, &paypalOrderCreatedAt, &sub.PayPalStatus, &sub.PayPalDetails,
		&sub.Submitted, &submittedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to scan membership: %w", err)
	}

	if err := r.populateMembershipFromJSON(&sub, submissionDate, paypalOrderCreatedAt, submittedAt,
		studentsJSON, interestsJSON, addonsJSON, feesJSON); err != nil {
		return nil, fmt.Errorf("failed to populate membership from JSON: %w", err)
	}

	return &sub, nil
}

func (r *MembershipRepository) populateMembershipFromJSON(sub *MembershipSubmission,
	submissionDate, paypalOrderCreatedAt, submittedAt sql.NullString,
	studentsJSON, interestsJSON, addonsJSON, feesJSON sql.NullString) error {

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

	if err := unmarshalNullableJSON(interestsJSON, &sub.Interests); err != nil {
		return fmt.Errorf("failed to unmarshal interests: %w", err)
	}

	if err := unmarshalNullableJSON(addonsJSON, &sub.Addons); err != nil {
		return fmt.Errorf("failed to unmarshal addons: %w", err)
	}

	if err := unmarshalNullableJSON(feesJSON, &sub.Fees); err != nil {
		return fmt.Errorf("failed to unmarshal fees: %w", err)
	}

	return nil
}

// =============================================================================
// UPDATE OPERATIONS
// =============================================================================

// PayPal updates

func (r *MembershipRepository) UpdatePayPalOrder(formID, orderID string, createdAt *time.Time) error {
	const stmt = `UPDATE membership_submissions SET paypal_order_id = ?, paypal_order_created_at = ? WHERE form_id = ?`

	_, err := ExecDB(stmt, orderID, formatNullableTime(createdAt), formID)
	if err != nil {
		return fmt.Errorf("failed to update PayPal order: %w", err)
	}

	return nil
}

func (r *MembershipRepository) UpdatePayPalCapture(formID, paypalDetails, status string, submittedAt *time.Time) error {
	const stmt = `
		UPDATE membership_submissions
		SET paypal_details = ?, paypal_status = ?, submitted = 1, submitted_at = ?
		WHERE form_id = ?`

	_, err := ExecDB(stmt, paypalDetails, status, formatNullableTime(submittedAt), formID)
	if err != nil {
		return fmt.Errorf("failed to update PayPal capture: %w", err)
	}

	return nil
}

func (r *MembershipRepository) UpdatePayPalDetails(formID, payPalStatus, payPalWebhook string) error {
	const stmt = `UPDATE membership_submissions SET paypal_status = ?, paypal_webhook = ? WHERE form_id = ?`

	_, err := ExecDB(stmt, payPalStatus, payPalWebhook, formID)
	if err != nil {
		return fmt.Errorf("failed to update PayPal details: %w", err)
	}

	return nil
}

// Payment updates

func (r *MembershipRepository) UpdatePayment(sub MembershipSubmission) error {
	addonsJSON, err := marshalJSON(sub.Addons)
	if err != nil {
		return fmt.Errorf("failed to marshal addons: %w", err)
	}

	feesJSON, err := marshalJSON(sub.Fees)
	if err != nil {
		return fmt.Errorf("failed to marshal fees: %w", err)
	}

	const stmt = `
		UPDATE membership_submissions 
		SET membership = ?, addons_json = ?, fees_json = ?, donation = ?, 
			cover_fees = ?, calculated_amount = ?, submitted = ?, submitted_at = ? 
		WHERE form_id = ?`

	_, err = ExecDB(stmt,
		sub.Membership, addonsJSON, feesJSON, sub.Donation,
		sub.CoverFees, sub.CalculatedAmount, sub.Submitted,
		formatNullableTime(sub.SubmittedAt), sub.FormID,
	)

	if err != nil {
		return fmt.Errorf("failed to update membership payment: %w", err)
	}

	return nil
}

// Email updates

func (r *MembershipRepository) UpdateEmailStatus(formID string, confirmationSent, adminNotificationSent bool) error {
	now := time.Now()
	const stmt = `
        UPDATE membership_submissions 
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

func InsertMembership(sub MembershipSubmission) error {
	repo := NewMembershipRepository()
	return repo.Insert(sub)
}

func GetMembershipByID(formID string) (*MembershipSubmission, error) {
	repo := NewMembershipRepository()
	return repo.GetByID(formID)
}

func UpdatePayPalStatus(formID, orderID, status, details string, createdAt *time.Time) error {
	repo := NewMembershipRepository()
	if err := repo.UpdatePayPalOrder(formID, orderID, createdAt); err != nil {
		return err
	}
	return repo.UpdatePayPalDetails(formID, status, "")
}

func UpdateMembershipPayPalOrder(formID, orderID string, createdAt *time.Time) error {
	repo := NewMembershipRepository()
	return repo.UpdatePayPalOrder(formID, orderID, createdAt)
}

func UpdateMembershipPayPalCapture(formID, paypalDetails, status string, submittedAt *time.Time) error {
	repo := NewMembershipRepository()
	return repo.UpdatePayPalCapture(formID, paypalDetails, status, submittedAt)
}

func UpdateMembershipPayPalDetails(formID, payPalStatus, payPalWebhook string) error {
	repo := NewMembershipRepository()
	return repo.UpdatePayPalDetails(formID, payPalStatus, payPalWebhook)
}

func UpdateMembershipPayment(sub MembershipSubmission) error {
	repo := NewMembershipRepository()
	return repo.UpdatePayment(sub)
}

func UpdateMembershipEmailStatus(formID string, confirmationSent, adminNotificationSent bool) error {
	repo := NewMembershipRepository()
	return repo.UpdateEmailStatus(formID, confirmationSent, adminNotificationSent)
}

func GetMembershipsByYear(year int) ([]MembershipSubmission, error) {
	repo := NewMembershipRepository()
	return repo.GetByYear(year)
}
