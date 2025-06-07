package testing

import (
	"fmt"
	"strings"
	"time"

	"sbcbackend/internal/data"
)

// TestMembershipData generates test membership submission data
type TestMembershipData struct {
	FormID           string
	AccessToken      string
	FullName         string
	Email            string
	School           string
	Membership       string
	MembershipStatus string
	Students         []data.Student
	Addons           []string
	Fees             map[string]int
	Donation         float64
	CoverFees        bool
}

// GenerateTestMembership creates a test membership submission
func (ts *TestSuite) GenerateTestMembership(variations ...string) TestMembershipData {
	formID := ts.GenerateFormID("membership")
	token, _ := ts.GenerateAccessToken(formID, "membership")

	// Default test data
	testData := TestMembershipData{
		FormID:           formID,
		AccessToken:      token,
		FullName:         "Jane Smith",
		Email:            "jane.smith@testschool.edu",
		School:           "lincoln-elementary",
		Membership:       "Basic Membership",
		MembershipStatus: "returning",
		Students: []data.Student{
			{Name: "Emma Smith", Grade: "3"},
			{Name: "Liam Smith", Grade: "5"},
		},
		Addons:    []string{"T-Shirt"},
		Fees:      map[string]int{"Spring Festival Fee": 1},
		Donation:  10.0,
		CoverFees: true,
	}

	// Apply variations
	for _, variation := range variations {
		switch variation {
		case "premium":
			testData.Membership = "Premium Membership"
			testData.Addons = append(testData.Addons, "Sticker Pack")
		case "no_fees":
			testData.Fees = make(map[string]int)
		case "no_donation":
			testData.Donation = 0
		case "single_student":
			testData.Students = testData.Students[:1]
		case "many_students":
			for i := 3; i <= 5; i++ {
				testData.Students = append(testData.Students, data.Student{
					Name:  fmt.Sprintf("Student %d", i),
					Grade: fmt.Sprintf("%d", i),
				})
			}
		case "invalid_email":
			testData.Email = "invalid-email"
		case "empty_name":
			testData.FullName = ""
		}
	}

	return testData
}

// ToMembershipSubmission converts test data to data.MembershipSubmission
func (td TestMembershipData) ToMembershipSubmission() data.MembershipSubmission {
	now := time.Now()
	return data.MembershipSubmission{
		FormID:           td.FormID,
		AccessToken:      td.AccessToken,
		SubmissionDate:   now,
		FullName:         td.FullName,
		FirstName:        getFirstName(td.FullName),
		LastName:         getLastName(td.FullName),
		Email:            td.Email,
		School:           td.School,
		Membership:       td.Membership,
		MembershipStatus: td.MembershipStatus,
		StudentCount:     len(td.Students),
		Students:         td.Students,
		Addons:           td.Addons,
		Fees:             td.Fees,
		Donation:         td.Donation,
		CoverFees:        td.CoverFees,
		Submitted:        false, // Will be set to true after payment
	}
}

// TestEventData generates test event submission data
type TestEventData struct {
	FormID      string
	AccessToken string
	Event       string
	FullName    string
	Email       string
	School      string
	Students    []data.Student
	CoverFees   bool
}

// GenerateTestEvent creates a test event submission
func (ts *TestSuite) GenerateTestEvent(variations ...string) TestEventData {
	formID := ts.GenerateFormID("event")
	token, _ := ts.GenerateAccessToken(formID, "event")

	testData := TestEventData{
		FormID:      formID,
		AccessToken: token,
		Event:       "spring-festival",
		FullName:    "John Doe",
		Email:       "john.doe@testschool.edu",
		School:      "lincoln-elementary",
		Students: []data.Student{
			{Name: "Alice Doe", Grade: "4"},
		},
		CoverFees: false,
	}

	// Apply variations
	for _, variation := range variations {
		switch variation {
		case "multiple_students":
			testData.Students = append(testData.Students, data.Student{
				Name: "Bob Doe", Grade: "6",
			})
		case "cover_fees":
			testData.CoverFees = true
		}
	}

	return testData
}

// ToEventSubmission converts test data to data.EventSubmission
func (td TestEventData) ToEventSubmission() data.EventSubmission {
	now := time.Now()
	return data.EventSubmission{
		FormID:         td.FormID,
		AccessToken:    td.AccessToken,
		SubmissionDate: now,
		Event:          td.Event,
		FullName:       td.FullName,
		FirstName:      getFirstName(td.FullName),
		LastName:       getLastName(td.FullName),
		Email:          td.Email,
		School:         td.School,
		StudentCount:   len(td.Students),
		Students:       td.Students,
		CoverFees:      td.CoverFees,
		Submitted:      false,
	}
}

// TestFundraiserData generates test fundraiser submission data
type TestFundraiserData struct {
	FormID        string
	AccessToken   string
	FullName      string
	Email         string
	School        string
	Describe      string
	DonorStatus   string
	Students      []data.Student
	DonationItems []data.StudentDonation
	CoverFees     bool
}

// GenerateTestFundraiser creates a test fundraiser submission
func (ts *TestSuite) GenerateTestFundraiser(variations ...string) TestFundraiserData {
	formID := ts.GenerateFormID("fundraiser")
	token, _ := ts.GenerateAccessToken(formID, "fundraiser")

	testData := TestFundraiserData{
		FormID:      formID,
		AccessToken: token,
		FullName:    "Mary Johnson",
		Email:       "mary.johnson@testschool.edu",
		School:      "lincoln-elementary",
		Describe:    "household",
		DonorStatus: "returning",
		Students: []data.Student{
			{Name: "Sarah Johnson", Grade: "2"},
		},
		DonationItems: []data.StudentDonation{
			{StudentName: "Sarah Johnson", Amount: 25.0},
		},
		CoverFees: false,
	}

	// Apply variations
	for _, variation := range variations {
		switch variation {
		case "multiple_students":
			testData.Students = append(testData.Students, data.Student{
				Name: "Tom Johnson", Grade: "4",
			})
			testData.DonationItems = append(testData.DonationItems, data.StudentDonation{
				StudentName: "Tom Johnson", Amount: 30.0,
			})
		case "large_donation":
			testData.DonationItems[0].Amount = 500.0
		case "cover_fees":
			testData.CoverFees = true
		}
	}

	return testData
}

// ToFundraiserSubmission converts test data to data.FundraiserSubmission
func (td TestFundraiserData) ToFundraiserSubmission() data.FundraiserSubmission {
	now := time.Now()

	// Calculate totals
	totalAmount := 0.0
	for _, item := range td.DonationItems {
		totalAmount += item.Amount
	}

	calculatedAmount := totalAmount
	if td.CoverFees {
		calculatedAmount += totalAmount*0.02 + 0.49
	}

	return data.FundraiserSubmission{
		FormID:           td.FormID,
		AccessToken:      td.AccessToken,
		SubmissionDate:   now,
		FullName:         td.FullName,
		FirstName:        getFirstName(td.FullName),
		LastName:         getLastName(td.FullName),
		Email:            td.Email,
		School:           td.School,
		Describe:         td.Describe,
		DonorStatus:      td.DonorStatus,
		StudentCount:     len(td.Students),
		Students:         td.Students,
		DonationItems:    td.DonationItems,
		TotalAmount:      totalAmount,
		CoverFees:        td.CoverFees,
		CalculatedAmount: calculatedAmount,
		Submitted:        false,
	}
}

// Utility functions
func getFirstName(fullName string) string {
	parts := strings.Split(fullName, " ")
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

func getLastName(fullName string) string {
	parts := strings.Split(fullName, " ")
	if len(parts) > 1 {
		return strings.Join(parts[1:], " ")
	}
	return ""
}
