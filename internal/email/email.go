// internal/email/email.go
package email

import (
	"bytes"
	"fmt"
	"html/template"
	"os"
	"os/exec"
	"strings"
	"time"

	"sbcbackend/internal/data"
	"sbcbackend/internal/logger"
)

const (
	defaultAlertRecipient = "admin@yourdomain.org"
	defaultAlertSender    = "alerts@yourdomain.org"
)

// EmailConfig holds email configuration
type EmailConfig struct {
	AlertRecipient     string
	AlertSender        string
	ConfirmationSender string
	SendConfirmations  bool
	MockMode           bool
	LogEmails          bool
}

// LoadEmailConfig loads email configuration from environment variables
func LoadEmailConfig() EmailConfig {
	return EmailConfig{
		AlertRecipient:     getEnvOrDefault("EMAIL_ALERT_RECIPIENT", defaultAlertRecipient),
		AlertSender:        getEnvOrDefault("EMAIL_ALERT_SENDER", defaultAlertSender),
		ConfirmationSender: getEnvOrDefault("EMAIL_CONFIRMATION_SENDER", "noreply@yourdomain.org"),
		SendConfirmations:  getEnvOrDefault("SEND_CONFIRMATION_EMAILS", "true") == "true",
		MockMode:           getEnvOrDefault("EMAIL_MOCK_MODE", "false") == "true",
		LogEmails:          getEnvOrDefault("EMAIL_LOG_MODE", "true") == "true",
	}
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// MembershipConfirmationData holds data for membership confirmation emails
type MembershipConfirmationData struct {
	FormID           string
	FullName         string
	FirstName        string
	Email            string
	School           string
	Membership       string
	Students         []data.Student
	Addons           []string
	Fees             map[string]int
	Donation         float64
	CalculatedAmount float64
	CoverFees        bool
	PayPalOrderID    string
	SubmittedAt      *time.Time
	Year             int
}

// FundraiserConfirmationData holds data for fundraiser confirmation emails
type FundraiserConfirmationData struct {
	FormID           string
	FullName         string
	FirstName        string
	Email            string
	School           string
	Describe         string
	DonorStatus      string
	Students         []data.Student
	DonationItems    []data.StudentDonation
	TotalAmount      float64
	CalculatedAmount float64
	CoverFees        bool
	PayPalOrderID    string
	SubmittedAt      *time.Time
	Year             int
}

var confirmationTemplate = `Subject: Membership Confirmation - {{.Membership}}

Dear {{.FirstName}},

Thank you for your membership submission! We have successfully received your payment and processed your membership for {{.Year}}.

**Membership Details:**
- Name: {{.FullName}}
- Email: {{.Email}}
- School: {{.School}}
- Membership Type: {{.Membership}}
- Students: {{.StudentCount}}
{{range .Students}}  • {{.Name}} ({{.Grade}})
{{end}}
{{if .Addons}}
**Add-ons:**
{{range .Addons}}  • {{.}}
{{end}}
{{end}}
{{if gt .Donation 0.0}}
**Donation:** ${{printf "%.2f" .Donation}}
{{end}}

**Total Amount:** ${{printf "%.2f" .CalculatedAmount}}
**Payment ID:** {{.PayPalOrderID}}
**Submitted:** {{.SubmittedAt.Format "January 2, 2006 at 3:04 PM"}}

If you have any questions, please don't hesitate to contact us.

Best regards,
The Membership Team`

var fundraiserConfirmationTemplate = `Subject: Fundraiser Donation Confirmation

Dear {{.FirstName}},

Thank you for your Practice-a-thon donation to the HEBISD Suzuki Booster Club for {{.Year}}!

**Donation Details:**
- Name: {{.FullName}}
- Email: {{.Email}}
- School: {{.School}}
- Status: {{.DonorStatus}}
{{if .Students}}
- Students:
{{range .Students}}  • {{.Name}} ({{.Grade}})
{{end}}{{end}}
{{if .DonationItems}}
- Donations:
{{range .DonationItems}}  • {{.StudentName}}: ${{printf "%.2f" .Amount}}
{{end}}{{end}}
**Total Amount:** ${{printf "%.2f" .TotalAmount}}
{{if .CoverFees}}
You generously covered the transaction fees—thank you!
{{end}}
**Payment ID:** {{.PayPalOrderID}}
**Submitted:** {{if .SubmittedAt}}{{.SubmittedAt.Format "January 2, 2006 at 3:04 PM"}}{{end}}

If you have any questions, please contact us.

Best regards,
The Booster Club Team
`

// SendMembershipConfirmation sends a confirmation email for a membership submission
func SendMembershipConfirmation(config EmailConfig, data MembershipConfirmationData) error {
	if !config.SendConfirmations {
		logger.LogInfo("Confirmation emails disabled, skipping email for %s", data.FormID)
		return nil
	}

	// Add student count for template
	templateData := struct {
		MembershipConfirmationData
		StudentCount int
	}{
		MembershipConfirmationData: data,
		StudentCount:               len(data.Students),
	}

	tmpl, err := template.New("confirmation").Parse(confirmationTemplate)
	if err != nil {
		return fmt.Errorf("failed to parse confirmation template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, templateData); err != nil {
		return fmt.Errorf("failed to execute confirmation template: %w", err)
	}

	// Extract subject from template output
	content := buf.String()
	lines := strings.Split(content, "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "Subject: ") {
		return fmt.Errorf("invalid template format: missing subject line")
	}

	subject := strings.TrimPrefix(lines[0], "Subject: ")
	body := strings.Join(lines[2:], "\n") // Skip subject and empty line

	logger.LogInfo("Sending confirmation email to %s for form %s", data.Email, data.FormID)

	if err := SendMail(data.Email, config.ConfirmationSender, subject, body); err != nil {
		logger.LogError("Failed to send confirmation email to %s: %v", data.Email, err)
		return fmt.Errorf("failed to send confirmation email: %w", err)
	}

	logger.LogInfo("Confirmation email sent successfully to %s", data.Email)
	return nil
}

// SendFundraiserConfirmation sends a confirmation email for a fundraiser submission
func SendFundraiserConfirmation(config EmailConfig, data FundraiserConfirmationData) error {
	if !config.SendConfirmations {
		logger.LogInfo("Fundraiser confirmation emails disabled, skipping email for %s", data.FormID)
		return nil
	}

	tmpl, err := template.New("fundraiserConfirmation").Parse(fundraiserConfirmationTemplate)
	if err != nil {
		return fmt.Errorf("failed to parse fundraiser confirmation template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute fundraiser confirmation template: %w", err)
	}

	content := buf.String()
	lines := strings.Split(content, "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "Subject: ") {
		return fmt.Errorf("invalid template format: missing subject line")
	}

	subject := strings.TrimPrefix(lines[0], "Subject: ")
	body := strings.Join(lines[2:], "\n") // Skip subject and empty line

	logger.LogInfo("Sending fundraiser confirmation email to %s for form %s", data.Email, data.FormID)

	if err := SendMail(data.Email, config.ConfirmationSender, subject, body); err != nil {
		logger.LogError("Failed to send fundraiser confirmation email to %s: %v", data.Email, err)
		return fmt.Errorf("failed to send fundraiser confirmation email: %w", err)
	}

	logger.LogInfo("Fundraiser confirmation email sent successfully to %s", data.Email)
	return nil
}

// SendAlertEmail sends an alert email to administrators
func SendAlertEmail(subject, body string) error {
	config := LoadEmailConfig()
	return SendMail(config.AlertRecipient, config.AlertSender, subject, body)
}

// SendMail sends an email using sendmail or logs it in mock mode
func SendMail(to, from, subject, body string) error {
	config := LoadEmailConfig()

	// Mock mode - just log to console with nice formatting
	if config.MockMode {
		logger.LogInfo("📧 ========== MOCK EMAIL ==========")
		logger.LogInfo("📬 To: %s", to)
		logger.LogInfo("📮 From: %s", from)
		logger.LogInfo("📄 Subject: %s", subject)
		logger.LogInfo("📝 Body:")
		logger.LogInfo("---")

		// Log body with proper line breaks
		bodyLines := strings.Split(body, "\n")
		for _, line := range bodyLines {
			logger.LogInfo("   %s", line)
		}

		logger.LogInfo("---")
		logger.LogInfo("✅ Mock email logged successfully")
		logger.LogInfo("📧 ==============================")
		return nil
	}

	// Log email attempt in non-mock mode
	if config.LogEmails {
		logger.LogInfo("Sending real email to %s with subject: %s", to, subject)
	}

	// Real email sending using sendmail
	headers := []string{
		fmt.Sprintf("From: %s", from),
		fmt.Sprintf("To: %s", to),
		fmt.Sprintf("Subject: %s", subject),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=\"utf-8\"",
		"",
	}

	message := strings.Join(headers, "\r\n") + body
	cmd := exec.Command("/usr/sbin/sendmail", "-t")
	cmd.Stdin = bytes.NewBufferString(message)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sendmail command failed: %w", err)
	}

	if config.LogEmails {
		logger.LogInfo("Real email sent successfully to %s", to)
	}

	return nil
}

// SendAdminNotification sends a notification to admins about new submissions
func SendAdminNotification(config EmailConfig, data MembershipConfirmationData) error {
	subject := fmt.Sprintf("New Membership: %s - %s", data.FullName, data.School)

	body := fmt.Sprintf(`New membership submission received:

Form ID: %s
Name: %s
Email: %s
School: %s
Membership: %s
Students: %d
Amount: $%.2f
Payment ID: %s
Submitted: %s

Students:
%s

Dashboard: https://yourdomain.com/info?year=%d
`,
		data.FormID,
		data.FullName,
		data.Email,
		data.School,
		data.Membership,
		len(data.Students),
		data.CalculatedAmount,
		data.PayPalOrderID,
		data.SubmittedAt.Format("January 2, 2006 at 3:04 PM"),
		formatStudentsList(data.Students),
		data.Year,
	)

	return SendMail(config.AlertRecipient, config.AlertSender, subject, body)
}

func SendFundraiserAdminNotification(config EmailConfig, data FundraiserConfirmationData) error {
	subject := fmt.Sprintf("New Fundraiser Donation: %s - %s", data.FullName, data.School)

	body := fmt.Sprintf(`New fundraiser donation received:

Form ID: %s
Name: %s
Email: %s
School: %s
Status: %s
Amount: $%.2f
Payment ID: %s
Submitted: %s

Students:
%s

Dashboard: https://yourdomain.com/info?year=%d
`,
		data.FormID,
		data.FullName,
		data.Email,
		data.School,
		data.DonorStatus,
		data.TotalAmount,
		data.PayPalOrderID,
		func() string {
			if data.SubmittedAt != nil {
				return data.SubmittedAt.Format("January 2, 2006 at 3:04 PM")
			}
			return ""
		}(),
		formatStudentsList(data.Students),
		data.Year,
	)

	return SendMail(config.AlertRecipient, config.AlertSender, subject, body)
}

func formatStudentsList(students []data.Student) string {
	if len(students) == 0 {
		return "  (No students listed)"
	}

	var lines []string
	for _, student := range students {
		lines = append(lines, fmt.Sprintf("  • %s (%s)", student.Name, student.Grade))
	}
	return strings.Join(lines, "\n")
}

// TestEmailFunctionality sends test emails to verify the system works
func TestEmailFunctionality() error {
	logger.LogInfo("🧪 Starting email functionality test...")

	// Create test data
	testData := MembershipConfirmationData{
		FormID:     "test-form-12345",
		FullName:   "Jane Smith",
		FirstName:  "Jane",
		Email:      "jane.smith@testschool.edu",
		School:     "Lincoln Elementary School",
		Membership: "Individual Teacher Membership",
		Students: []data.Student{
			{Name: "Emma Johnson", Grade: "3rd Grade"},
			{Name: "Liam Davis", Grade: "4th Grade"},
		},
		Addons:           []string{"Workshop Materials", "Digital Resources"},
		Fees:             map[string]int{"Processing Fee": 1},
		Donation:         15.00,
		CalculatedAmount: 89.50,
		CoverFees:        true,
		PayPalOrderID:    "TEST-PAYPAL-ORDER-789",
		SubmittedAt:      timePtr(time.Now()),
		Year:             time.Now().Year(),
	}

	config := LoadEmailConfig()

	// Test 1: Send confirmation email
	logger.LogInfo("🧪 Test 1: Sending confirmation email...")
	if err := SendMembershipConfirmation(config, testData); err != nil {
		logger.LogError("❌ Confirmation email test failed: %v", err)
		return err
	}
	logger.LogInfo("✅ Confirmation email test passed")

	// Test 2: Send admin notification
	logger.LogInfo("🧪 Test 2: Sending admin notification...")
	if err := SendAdminNotification(config, testData); err != nil {
		logger.LogError("❌ Admin notification test failed: %v", err)
		return err
	}
	logger.LogInfo("✅ Admin notification test passed")

	// Test 3: Send alert email
	logger.LogInfo("🧪 Test 3: Sending alert email...")
	if err := SendAlertEmail("Test Alert - System Check", "This is a test alert message to verify the email system is working correctly."); err != nil {
		logger.LogError("❌ Alert email test failed: %v", err)
		return err
	}
	logger.LogInfo("✅ Alert email test passed")

	logger.LogInfo("🎉 All email tests completed successfully!")
	return nil
}

func timePtr(t time.Time) *time.Time {
	return &t
}
