// internal/data/data.go
package data

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"sbcbackend/internal/config"
	"sbcbackend/internal/logger"
)

type MembershipSummary struct {
	TotalSubmissions       int
	MembershipStatusCounts map[string]int
	DescribeCounts         map[string]int
	MembershipLevelCounts  map[string]int
	SchoolCounts           map[string]int
	InterestsCounts        map[string]int
	StudentSummary         StudentStats
	FinancialSummary       FinancialStats
}

type StudentStats struct {
	TotalStudents                int
	AverageStudentsPerSubmission float64
}

type FinancialStats struct {
	TotalAmount     float64
	TotalPayPalFees float64
	TotalDonation   float64
}

type FundraiserSummary struct {
	TotalSubmissions int
	TotalAmount      float64
	TotalStudents    int
	SchoolCounts     map[string]int
	DonorCounts      map[string]int
}

type InterestPerson struct {
	FullName string
	School   string
	Email    string
}

type MembershipExtras struct {
	Interests      map[string][]InterestPerson
	AddOnPurchases []AddOnPurchase // ADD THIS if it's missing
	FeePurchases   []FeePurchase   // NEW: Add this line
}

type FeePurchase struct {
	FullName         string
	School           string
	StudentNames     string // Comma-separated student names
	FeeName          string
	Quantity         int
	AmountPaid       float64
	PayPalOrderID    string
	PayPalCaptureID  string
	PayPalCaptureURL string
}

// ADD this struct if it doesn't exist:
type AddOnPurchase struct {
	FullName string
	School   string
	Item     string
	Date     string
}

// GetCurrentTimeInZone returns the current time formatted in the specified time zone.
func GetCurrentTimeInZone(loc *time.Location) string {
	currentTime := time.Now().In(loc)
	return currentTime.Format("2006-01-02 15:04:05 MST")
}

// ComputeMembershipSummary aggregates summary stats from all membership entries.
func ComputeMembershipSummary(entries []MembershipSubmission) (MembershipSummary, MembershipExtras) {
	summary := MembershipSummary{
		MembershipStatusCounts: make(map[string]int),
		DescribeCounts:         make(map[string]int),
		MembershipLevelCounts:  make(map[string]int),
		SchoolCounts:           make(map[string]int),
		InterestsCounts:        make(map[string]int),
	}
	extras := MembershipExtras{
		Interests:      make(map[string][]InterestPerson),
		AddOnPurchases: []AddOnPurchase{},
		FeePurchases:   []FeePurchase{},
	}
	var totalStudents int
	var totalAmount float64
	var totalDonation float64
	var totalPayPalFees float64

	for i, entry := range entries {
		summary.TotalSubmissions++
		summary.MembershipStatusCounts[entry.MembershipStatus]++
		summary.DescribeCounts[entry.Describe]++
		summary.MembershipLevelCounts[entry.Membership]++
		summary.SchoolCounts[entry.School]++
		totalStudents += entry.StudentCount
		totalAmount += entry.CalculatedAmount
		totalDonation += entry.Donation

		// Process add-on purchases for this entry
		for _, addon := range entry.Addons {
			if addon != "" {
				extras.AddOnPurchases = append(extras.AddOnPurchases, AddOnPurchase{
					FullName: entry.FullName,
					School:   entry.School,
					Item:     addon,
					Date:     entry.SubmissionDate.Format("2006-01-02"),
				})
			}
		}

		// Parse interests from the entry if they exist
		for _, interest := range entry.Interests {
			if interest != "" {
				extras.Interests[interest] = append(extras.Interests[interest], InterestPerson{
					FullName: entry.FullName,
					School:   entry.School,
					Email:    entry.Email,
				})
				summary.InterestsCounts[interest]++
			}
		}

		// ENHANCED: Extract all PayPal data and populate computed fields
		if entry.PayPalDetails != "" {
			// logger.LogInfo("Processing PayPal details for %s", entry.FormID)
			email, captureID, captureURL, fee := extractPayPalDataFromJSON(entry.PayPalDetails, entry.FormID)

			// Update the entry with computed PayPal fields
			entries[i].PayPalEmail = email
			entries[i].PayPalCaptureID = captureID
			entries[i].PayPalCaptureURL = captureURL
			entries[i].PayPalFee = fee

			totalPayPalFees += fee
		} else {
			logger.LogInfo("No PayPal details for %s - payment not completed", entry.FormID)
			// Explicitly set zero values
			entries[i].PayPalEmail = ""
			entries[i].PayPalCaptureID = ""
			entries[i].PayPalCaptureURL = ""
			entries[i].PayPalFee = 0.0
		}

		// Process fee purchases for this entry
		if len(entries[i].Fees) > 0 {
			// Build student names string
			var studentNames []string
			for _, student := range entries[i].Students {
				if student.Name != "" {
					studentNames = append(studentNames, student.Name)
				}
			}
			studentNamesStr := strings.Join(studentNames, ", ")
			if studentNamesStr == "" {
				studentNamesStr = "(No students listed)"
			}

			// Load fee prices to calculate amount paid per fee
			feesPath := config.GetEnvBasedSetting("FEES_JSON_PATH")
			if feesPath == "" {
				feesPath = "/home/public/static/fees.json" // Fallback default
			}
			feesPrices, err := LoadNamePriceMap(feesPath)
			if err != nil {
				logger.LogWarn("Could not load fee prices for summary calculation: %v", err)
				feesPrices = make(map[string]float64) // Use empty map as fallback
			}

			for feeName, quantity := range entries[i].Fees {
				if quantity > 0 {
					pricePerFee := feesPrices[feeName]
					totalFeeAmount := pricePerFee * float64(quantity)

					extras.FeePurchases = append(extras.FeePurchases, FeePurchase{
						FullName:         entries[i].FullName,
						School:           entries[i].School,
						StudentNames:     studentNamesStr,
						FeeName:          feeName,
						Quantity:         quantity,
						AmountPaid:       totalFeeAmount,
						PayPalOrderID:    entries[i].PayPalOrderID,
						PayPalCaptureID:  entries[i].PayPalCaptureID,
						PayPalCaptureURL: entries[i].PayPalCaptureURL,
					})
				}
			}
		}
	}

	// Calculate averages (handle division by zero)
	avgStudentsPerSubmission := 0.0
	if len(entries) > 0 {
		avgStudentsPerSubmission = float64(totalStudents) / float64(len(entries))
	}

	summary.StudentSummary = StudentStats{
		TotalStudents:                totalStudents,
		AverageStudentsPerSubmission: avgStudentsPerSubmission,
	}
	summary.FinancialSummary = FinancialStats{
		TotalAmount:     totalAmount,
		TotalPayPalFees: totalPayPalFees,
		TotalDonation:   totalDonation,
	}

	return summary, extras
}

// Add this enhanced PayPal data extraction function to your data.go:
func extractPayPalDataFromJSON(paypalDetailsJSON, formID string) (email, captureID, captureURL string, fee float64) {
	// Return zeros/empty strings for empty data - this is normal
	if paypalDetailsJSON == "" || paypalDetailsJSON == "null" {
		logger.LogInfo("No PayPal details for %s (payment not completed)", formID)
		return "", "", "", 0.0
	}

	var paypalData map[string]interface{}
	if err := json.Unmarshal([]byte(paypalDetailsJSON), &paypalData); err != nil {
		logger.LogWarn("Failed to parse PayPal details JSON for %s: %v", formID, err)
		return "", "", "", 0.0
	}

	// Extract PayPal email from payer info
	if payer, ok := paypalData["payer"].(map[string]interface{}); ok {
		if emailAddr, ok := payer["email_address"].(string); ok {
			email = emailAddr
		}
	}

	// Navigate to capture data: purchase_units[0].payments.captures[0]
	purchaseUnits, ok := paypalData["purchase_units"].([]interface{})
	if !ok || len(purchaseUnits) == 0 {
		logger.LogWarn("No purchase_units found in PayPal data for %s", formID)
		return email, "", "", 0.0
	}

	firstUnit, ok := purchaseUnits[0].(map[string]interface{})
	if !ok {
		logger.LogWarn("Invalid purchase_units[0] structure for %s", formID)
		return email, "", "", 0.0
	}

	payments, ok := firstUnit["payments"].(map[string]interface{})
	if !ok {
		logger.LogWarn("No payments found in PayPal data for %s", formID)
		return email, "", "", 0.0
	}

	captures, ok := payments["captures"].([]interface{})
	if !ok || len(captures) == 0 {
		logger.LogWarn("No captures found in PayPal data for %s", formID)
		return email, "", "", 0.0
	}

	firstCapture, ok := captures[0].(map[string]interface{})
	if !ok {
		logger.LogWarn("Invalid captures[0] structure for %s", formID)
		return email, "", "", 0.0
	}

	// Extract capture ID
	if capID, ok := firstCapture["id"].(string); ok {
		captureID = capID
	}

	// Extract capture URL from links
	if links, ok := firstCapture["links"].([]interface{}); ok {
		for _, link := range links {
			if linkMap, ok := link.(map[string]interface{}); ok {
				if rel, ok := linkMap["rel"].(string); ok && rel == "self" {
					if href, ok := linkMap["href"].(string); ok {
						captureURL = href
					}
				}
			}
		}
	}

	// Extract PayPal fee
	if sellerBreakdown, ok := firstCapture["seller_receivable_breakdown"].(map[string]interface{}); ok {
		if paypalFee, ok := sellerBreakdown["paypal_fee"].(map[string]interface{}); ok {
			if feeValue, ok := paypalFee["value"].(string); ok {
				if parsedFee, err := strconv.ParseFloat(feeValue, 64); err == nil {
					fee = parsedFee
				}
			}
		}
	}

	// logger.LogInfo("Extracted PayPal data for %s - Email: %s, Capture: %s, Fee: $%.2f",
	// 		formID, email, captureID, fee)

	return email, captureID, captureURL, fee
}

// ProcessFundraiserPaymentData handles payment processing for fundraiser submissions
// This is the fundraiser equivalent of the /save-payment-data endpoint logic
func ProcessFundraiserPayment(sub *FundraiserSubmission) error {
	// Validate the payment data
	if err := ValidateFundraiserPayment(*sub); err != nil {
		return fmt.Errorf("fundraiser payment validation failed: %w", err)
	}

	// Recalculate totals to prevent tampering (similar to membership flow)
	calculatedTotal := 0.0
	for _, donation := range sub.DonationItems {
		calculatedTotal += donation.Amount
	}

	// Round to 2 decimal places
	calculatedTotal = float64(int(calculatedTotal*100+0.5)) / 100

	// Verify the submitted total matches our calculation
	if math.Abs(sub.TotalAmount-calculatedTotal) > 0.01 {
		return fmt.Errorf("total amount mismatch: expected %.2f, got %.2f", calculatedTotal, sub.TotalAmount)
	}

	// Calculate final amount with fees
	finalAmount := sub.TotalAmount
	if sub.CoverFees {
		feeAmount := sub.TotalAmount*0.02 + 0.49
		finalAmount += feeAmount
	}
	finalAmount = float64(int(finalAmount*100+0.5)) / 100

	// Verify calculated amount
	if math.Abs(sub.CalculatedAmount-finalAmount) > 0.01 {
		return fmt.Errorf("calculated amount mismatch: expected %.2f, got %.2f", finalAmount, sub.CalculatedAmount)
	}

	// Update the submission with verified amounts
	sub.TotalAmount = calculatedTotal
	sub.CalculatedAmount = finalAmount

	// Save the updated payment data to database
	if err := UpdateFundraiserPayment(*sub); err != nil {
		return fmt.Errorf("failed to update fundraiser payment data: %w", err)
	}

	logger.LogInfo("Fundraiser payment data processed for %s: Total=%.2f, Final=%.2f",
		sub.FormID, sub.TotalAmount, sub.CalculatedAmount)

	return nil
}

// ValidateFundraiserPaymentData validates fundraiser payment data
func ValidateFundraiserPayment(sub FundraiserSubmission) error {
	var errors []string

	// Validate basic required fields
	if sub.FormID == "" {
		errors = append(errors, "form ID is required")
	}

	if sub.Email == "" {
		errors = append(errors, "email is required")
	}

	if sub.StudentCount <= 0 {
		errors = append(errors, "student count must be greater than 0")
	}

	// Validate donation items
	if len(sub.DonationItems) == 0 {
		errors = append(errors, "at least one donation item is required")
	}

	if len(sub.DonationItems) != sub.StudentCount {
		errors = append(errors, fmt.Sprintf("donation items count (%d) doesn't match student count (%d)",
			len(sub.DonationItems), sub.StudentCount))
	}

	// Validate individual donation amounts
	totalCalculated := 0.0
	for i, donation := range sub.DonationItems {
		if donation.StudentName == "" {
			errors = append(errors, fmt.Sprintf("donation item %d: student name is required", i+1))
		}

		if donation.Amount <= 0 {
			errors = append(errors, fmt.Sprintf("donation item %d (%s): amount must be greater than 0",
				i+1, donation.StudentName))
		}

		if donation.Amount > 1000 {
			errors = append(errors, fmt.Sprintf("donation item %d (%s): amount exceeds maximum of $1000",
				i+1, donation.StudentName))
		}

		totalCalculated += donation.Amount
	}

	// Validate total amount
	if sub.TotalAmount <= 0 {
		errors = append(errors, "total amount must be greater than 0")
	}

	// Validate calculated amount
	if sub.CalculatedAmount <= 0 {
		errors = append(errors, "calculated amount must be greater than 0")
	}

	// Validate amount relationships
	expectedTotal := float64(int(totalCalculated*100+0.5)) / 100
	if math.Abs(sub.TotalAmount-expectedTotal) > 0.01 {
		errors = append(errors, fmt.Sprintf("total amount validation failed: expected %.2f, got %.2f",
			expectedTotal, sub.TotalAmount))
	}

	expectedCalculated := sub.TotalAmount
	if sub.CoverFees {
		expectedCalculated += sub.TotalAmount*0.02 + 0.49
	}
	expectedCalculated = float64(int(expectedCalculated*100+0.5)) / 100

	if math.Abs(sub.CalculatedAmount-expectedCalculated) > 0.01 {
		errors = append(errors, fmt.Sprintf("calculated amount validation failed: expected %.2f, got %.2f",
			expectedCalculated, sub.CalculatedAmount))
	}

	// Check for reasonable donation limits
	if sub.CalculatedAmount > 10000 {
		errors = append(errors, "donation amount exceeds reasonable limit of $10,000")
	}

	if len(errors) > 0 {
		return fmt.Errorf("validation errors: %s", strings.Join(errors, "; "))
	}

	return nil
}

// GetFundraiserPaymentDetails retrieves and validates fundraiser payment details
// This can be used by other parts of the system that need fundraiser payment info
func GetFundraiserPaymentDetails(formID string) (*FundraiserSubmission, error) {
	sub, err := GetFundraiserByID(formID)
	if err != nil {
		return nil, fmt.Errorf("failed to get fundraiser by ID: %w", err)
	}

	// Validate the payment data is consistent
	if err := ValidateFundraiserPayment(*sub); err != nil {
		logger.LogWarn("Fundraiser payment data validation failed for %s: %v", formID, err)
		// Don't return error here - just log the warning
		// The data might be valid enough for display purposes
	}

	return sub, nil
}

// LoadNamePriceMap reads a JSON file like products.json or memberships.json
// and returns a map of item name to its price (float64).
func LoadNamePriceMap(filePath string) (map[string]float64, error) {
	fileBytes, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("reading file: %w", err)
	}

	var entries []struct {
		Name  string  `json:"name"`
		Price float64 `json:"price"`
	}

	err = json.Unmarshal(fileBytes, &entries)
	if err != nil {
		return nil, fmt.Errorf("parsing JSON: %w", err)
	}

	result := make(map[string]float64)
	for _, entry := range entries {
		result[entry.Name] = entry.Price
	}
	return result, nil
}

// LoadValidNames loads valid names (memberships/products) from a JSON file.
// Used to display JSON inventory lists on checkout pages.
func LoadValidNames(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var items []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}

	valid := make(map[string]bool)
	for _, item := range items {
		valid[item.Name] = true
	}
	return valid, nil
}
