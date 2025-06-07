// internal/order/common.go
package order

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"sbcbackend/internal/data"
	"sbcbackend/internal/inventory"
)

// Variables

// Global inventory service for order handlers
var inventoryService *inventory.Service

// SetInventoryService injects the inventory service
func SetInventoryService(service *inventory.Service) {
	inventoryService = service
}

// Template variables and function maps
var eventOrderSummaryTmpl = template.Must(template.New("event_order_summary.html.tmpl").
	Funcs(template.FuncMap{
		"capitalize": func(s string) string {
			if s == "" {
				return ""
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
		"formatDateTime": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.Local().Format("Jan 2, 2006 3:04pm")
		},
		"formatDisplayName": formatDisplayName,
		"formatCurrency": func(amount float64) string {
			return fmt.Sprintf("$%.2f", amount)
		},
		"getenv": func(key string) string {
			return os.Getenv(key)
		},
		"currentYear": func() int { // ADD THIS LINE
			return time.Now().Year()
		},
		"sub": func(a, b float64) float64 {
			return a - b
		},
		"lower": strings.ToLower,
	}).ParseFiles("templates/event_order_summary.html.tmpl"))

var eventSuccessTmpl = template.Must(template.New("event_success.html.tmpl").
	Funcs(template.FuncMap{
		"capitalize": func(s string) string {
			if s == "" {
				return ""
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
		"formatDisplayName": formatDisplayName,
		"formatDateTime": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.Local().Format("Jan 2, 2006 3:04pm")
		},
		"currentYear": func() int { // ADD THIS LINE
			return time.Now().Year()
		},
		"formatCurrency": func(amount float64) string {
			return fmt.Sprintf("$%.2f", amount)
		},
		"lower": strings.ToLower,
	}).ParseFiles("templates/event_success.html.tmpl"))

var orderSummaryTmpl = template.Must(template.New("order_summary.html.tmpl").
	Funcs(template.FuncMap{
		"capitalize": func(s string) string {
			if s == "" {
				return ""
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
		"formatDisplayName": formatDisplayName,
		"formatDateTime": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.Local().Format("Jan 2, 2006 3:04pm")
		},
		"currentYear": func() int { // ADD THIS LINE
			return time.Now().Year()
		},
	}).ParseFiles("templates/order_summary.html.tmpl"))

var successPageTmpl = template.Must(template.New("success.html.tmpl").
	Funcs(template.FuncMap{
		"capitalize": func(s string) string {
			if s == "" {
				return ""
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
		"formatDisplayName": formatDisplayName,
		"formatDateTime": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.Local().Format("Jan 2, 2006 3:04pm")
		},
		"currentYear": func() int { // ADD THIS LINE
			return time.Now().Year()
		},
		"formatCurrency": func(amount float64) string {
			return fmt.Sprintf("$%.2f", amount)
		},
	}).ParseFiles("templates/success.html.tmpl"))

var fundraiserSummaryTmpl = template.Must(template.New("fundraiser_order_summary.html.tmpl").
	Funcs(template.FuncMap{
		"capitalize": func(s string) string {
			if s == "" {
				return ""
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
		"formatDisplayName": formatDisplayName,
		"formatDateTime": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.Local().Format("Jan 2, 2006 3:04pm")
		},
		"currentYear": func() int { // ADD THIS LINE
			return time.Now().Year()
		},
		"formatCurrency": func(amount float64) string {
			return fmt.Sprintf("$%.2f", amount)
		},
	}).ParseFiles("templates/fundraiser_order_summary.html.tmpl"))

var fundraisersuccessTmpl = template.Must(template.New("fundraiser_success.html.tmpl").
	Funcs(template.FuncMap{
		"capitalize": func(s string) string {
			if s == "" {
				return ""
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
		"formatDateTime": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.Local().Format("Jan 2, 2006 3:04pm")
		},
		"currentYear": func() int { // ADD THIS LINE
			return time.Now().Year()
		},
		"formatCurrency": func(amount float64) string {
			return fmt.Sprintf("$%.2f", amount)
		},
	}).ParseFiles("templates/fundraiser_success.html.tmpl"))

// Types

// Helper functions

// showTokenExpiredPage displays a user-friendly token expiration page for any form type
func showTokenExpiredPage(w http.ResponseWriter, formType string) {
	var newFormLink string
	var newFormText string

	switch formType {
	case "membership":
		newFormLink = "/membership.html"
		newFormText = "📝 New Membership"
	case "event":
		newFormLink = "/event.html"
		newFormText = "📝 New Event Registration"
	case "fundraiser":
		newFormLink = "/fundraiser.html"
		newFormText = "📝 New Donation"
	default:
		newFormLink = "/"
		newFormText = "📝 New Form"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Session Expired</title>
    <link rel="stylesheet" href="/static/css/simple.css">
</head>
<body>
    <div class="container">
        <h1>Session Expired</h1>
        <p>Your session has expired for security reasons. Sessions are limited to 15 minutes to protect your personal information and payment data.</p>
        <p>If you completed your payment, a confirmation email should have been sent to you.</p>
        <p>Please return to the homepage to begin a new registration if needed.</p>
        <a href="/" class="button">🏠 Return to Homepage</a>
        <a href="%s" class="button">%s</a>
    </div>
</body>
</html>`, newFormLink, newFormText)
	w.Write([]byte(html))
}

// getFormTypeFromID extracts form type from formID prefix
func getFormTypeFromID(formID string) string {
	parts := strings.Split(formID, "-")
	if len(parts) > 0 {
		return parts[0]
	}
	return "unknown"
}

// formatDisplayName removes hyphens/underscores and properly capitalizes words
func formatDisplayName(input string) string {
	if input == "" {
		return ""
	}

	// Remove hyphens, underscores, and split into words
	words := strings.FieldsFunc(input, func(c rune) bool {
		return c == '-' || c == '_'
	})

	// Capitalize each word
	for i, word := range words {
		if len(word) > 0 {
			words[i] = strings.Title(strings.ToLower(word))
		}
	}

	return strings.Join(words, " ")
}

func formatReceiptID(formID string) string {
	// Convert "membership-2025-05-24_14-25-12-8I_VFQ" to something readable
	parts := strings.Split(formID, "-")
	if len(parts) >= 3 {
		return fmt.Sprintf("%s%s", strings.ToUpper(parts[2]), parts[len(parts)-1])
	}
	return formID
}

func formatStudentList(students []data.Student) string {
	if len(students) == 0 {
		return "None listed"
	}

	var names []string
	for _, student := range students {
		if student.Grade != "" {
			names = append(names, fmt.Sprintf("%s (%s)", student.Name, student.Grade))
		} else {
			names = append(names, student.Name)
		}
	}

	return strings.Join(names, ", ")
}

func formatList(items []string) string {
	if len(items) == 0 {
		return "None"
	}
	return strings.Join(items, ", ")
}

func formatFeesMap(fees map[string]int) string {
	if len(fees) == 0 {
		return "None"
	}

	var parts []string
	for name, count := range fees {
		if count > 1 {
			parts = append(parts, fmt.Sprintf("%s (×%d)", name, count))
		} else {
			parts = append(parts, name)
		}
	}

	return strings.Join(parts, ", ")
}

func calculateActualProcessingTime(sub *data.MembershipSubmission) string {
	if sub.SubmittedAt == nil {
		return "In progress"
	}

	duration := sub.SubmittedAt.Sub(sub.SubmissionDate)
	if duration < time.Minute {
		return fmt.Sprintf("%d seconds", int(duration.Seconds()))
	} else if duration < time.Hour {
		return fmt.Sprintf("%d minutes", int(duration.Minutes()))
	}
	return fmt.Sprintf("%.1f hours", duration.Hours())
}

// calculateProcessingFee calculates the processing fee based on total and whether fees are covered
func calculateProcessingFee(totalAmount float64, coverFees bool) float64 {
	if !coverFees {
		return 0.0
	}

	// Calculate what the original amount was before fees
	// If total = original * 1.02 + 0.49, then original = (total - 0.49) / 1.02
	originalAmount := (totalAmount - 0.49) / 1.02
	return totalAmount - originalAmount
}

func extractPayPalFee(paypalDetailsJSON string) float64 {
	if paypalDetailsJSON == "" || paypalDetailsJSON == "null" {
		return 0.0
	}

	var paypalData map[string]interface{}
	if err := json.Unmarshal([]byte(paypalDetailsJSON), &paypalData); err != nil {
		return 0.0
	}

	// Navigate to the fee: purchase_units[0].payments.captures[0].seller_receivable_breakdown.paypal_fee.value
	if purchaseUnits, ok := paypalData["purchase_units"].([]interface{}); ok && len(purchaseUnits) > 0 {
		if unit, ok := purchaseUnits[0].(map[string]interface{}); ok {
			if payments, ok := unit["payments"].(map[string]interface{}); ok {
				if captures, ok := payments["captures"].([]interface{}); ok && len(captures) > 0 {
					if capture, ok := captures[0].(map[string]interface{}); ok {
						if breakdown, ok := capture["seller_receivable_breakdown"].(map[string]interface{}); ok {
							if fee, ok := breakdown["paypal_fee"].(map[string]interface{}); ok {
								if value, ok := fee["value"].(string); ok {
									if amount, err := strconv.ParseFloat(value, 64); err == nil {
										return amount
									}
								}
							}
						}
					}
				}
			}
		}
	}

	return 0.0
}
