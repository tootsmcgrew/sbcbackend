// internal/order/event.go
package order

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"sbcbackend/internal/config"
	"sbcbackend/internal/data"
	"sbcbackend/internal/email"
	"sbcbackend/internal/logger"
	"sbcbackend/internal/security"
)

// EventItemDisplay represents a formatted event item for template display
type EventItemDisplay struct {
	StudentName string // For per-student items
	ItemName    string
	ItemLabel   string
	Quantity    int
	UnitPrice   float64
	TotalPrice  float64
	IsShared    bool
}

// checkout flow code goes below, from front to back

// parsing, processing

// summary pages

func handleEventOrderDetails(w http.ResponseWriter, r *http.Request, formID, token string) {
	sub, err := data.GetEventByID(formID)
	if err != nil {
		logger.LogError("GetEventByID failed for %s: %v", formID, err)
		http.Error(w, "Event details not found", http.StatusNotFound)
		return
	}

	// Validate access token matches
	if sub.AccessToken != token {
		logger.LogWarn("Access token mismatch for formID %s from %s", formID, logger.GetClientIP(r))
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Parse event selections for display
	eventSelections, eventItemsDisplay, totalFromSelections := parseEventSelectionsForDisplay(sub.FoodChoicesJSON, sub.Event)

	// Get student names for better display
	studentNames := make(map[string]string)
	for i, student := range sub.Students {
		studentNames[fmt.Sprintf("%d", i)] = student.Name
	}

	// Update student names in display items
	for i := range eventItemsDisplay {
		if !eventItemsDisplay[i].IsShared {
			studentIndex := eventItemsDisplay[i].StudentName // This should be "0", "1", etc.
			if realName, exists := studentNames[studentIndex]; exists {
				eventItemsDisplay[i].StudentName = realName
			}
		}
	}

	// Compose the struct for template
	resp := struct {
		FormID              string
		FormType            string
		Event               string
		FullName            string
		FirstName           string
		LastName            string
		Email               string
		School              string
		StudentCount        int
		Students            []data.Student
		EventSelections     interface{}        // Raw selections for API
		EventItemsDisplay   []EventItemDisplay // Formatted for display
		CalculatedAmount    float64
		CoverFees           bool
		ProcessingFee       float64
		FoodOrderID         string
		SubmittedAt         *time.Time
		TotalFromSelections float64
	}{
		FormID:              sub.FormID,
		FormType:            "event",
		Event:               sub.Event,
		FullName:            sub.FullName,
		FirstName:           sub.FirstName,
		LastName:            sub.LastName,
		Email:               sub.Email,
		School:              formatDisplayName(sub.School),
		StudentCount:        sub.StudentCount,
		Students:            sub.Students,
		EventSelections:     eventSelections,
		EventItemsDisplay:   eventItemsDisplay,
		CalculatedAmount:    sub.CalculatedAmount,
		CoverFees:           sub.CoverFees,
		ProcessingFee:       calculateProcessingFee(sub.CalculatedAmount, sub.CoverFees),
		FoodOrderID:         sub.FoodOrderID,
		SubmittedAt:         sub.SubmittedAt,
		TotalFromSelections: totalFromSelections,
	}

	logger.LogInfo("Event order details accessed for form %s", formID)

	// Render template or return JSON based on Accept header
	acceptHeader := r.Header.Get("Accept")
	if strings.Contains(acceptHeader, "text/html") || strings.HasSuffix(r.URL.Path, ".html") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := eventOrderSummaryTmpl.Execute(w, resp); err != nil {
			logger.LogError("Failed to render event order summary template: %v", err)
			http.Error(w, "Error rendering page", http.StatusInternalServerError)
		}
		return
	}

	// Return JSON (for API calls)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// Event-specific helpers (could stay in common or move to event package)
// parseEventSelectionsForDisplay parses the JSON and creates display-friendly data
func parseEventSelectionsForDisplay(foodChoicesJSON, eventName string) (interface{}, []EventItemDisplay, float64) {
	var eventSelections struct {
		StudentSelections map[string]map[string]bool `json:"student_selections"`
		SharedSelections  map[string]int             `json:"shared_selections"`
		CoverFees         bool                       `json:"cover_fees"`
	}

	var itemsDisplay []EventItemDisplay
	var total float64

	if foodChoicesJSON == "" {
		return eventSelections, itemsDisplay, total
	}

	if err := json.Unmarshal([]byte(foodChoicesJSON), &eventSelections); err != nil {
		logger.LogError("Failed to parse event selections: %v", err)
		return eventSelections, itemsDisplay, total
	}

	// Load event options to get prices and labels
	eventOptions := loadEventOptionsForDisplay(eventName)
	if eventOptions == nil {
		logger.LogError("Failed to load event options for %s", eventName)
		return eventSelections, itemsDisplay, total
	}

	// Process per-student selections
	if perStudentOptions, ok := eventOptions["per_student_options"].(map[string]interface{}); ok {
		for studentIndex, selections := range eventSelections.StudentSelections {
			// Change this line to just use the index for now - we'll replace it later
			studentName := studentIndex // We'll replace this in the calling function

			for optionKey, isSelected := range selections {
				if isSelected {
					if option, ok := perStudentOptions[optionKey].(map[string]interface{}); ok {
						label, _ := option["label"].(string)
						price, _ := option["price"].(float64)

						itemsDisplay = append(itemsDisplay, EventItemDisplay{
							StudentName: studentName, // This will be the index like "0", "1"
							ItemName:    optionKey,
							ItemLabel:   label,
							Quantity:    1,
							UnitPrice:   price,
							TotalPrice:  price,
							IsShared:    false,
						})
						total += price
					}
				}
			}
		}
	}

	// Process shared selections
	if sharedOptions, ok := eventOptions["shared_options"].(map[string]interface{}); ok {
		for optionKey, quantity := range eventSelections.SharedSelections {
			if quantity > 0 {
				if option, ok := sharedOptions[optionKey].(map[string]interface{}); ok {
					label, _ := option["label"].(string)
					price, _ := option["price"].(float64)
					totalPrice := price * float64(quantity)

					itemsDisplay = append(itemsDisplay, EventItemDisplay{
						StudentName: "",
						ItemName:    optionKey,
						ItemLabel:   label,
						Quantity:    quantity,
						UnitPrice:   price,
						TotalPrice:  totalPrice,
						IsShared:    true,
					})
					total += totalPrice
				}
			}
		}
	}

	return eventSelections, itemsDisplay, total
}

// loadEventOptionsForDisplay loads the event options JSON for display purposes
func loadEventOptionsForDisplay(eventName string) map[string]interface{} {
	eventOptionsPath := config.GetEnvBasedSetting("EVENT_OPTIONS_PATH")
	if eventOptionsPath == "" {
		eventOptionsPath = "/home/public/static/event-purchases.json"
	}

	eventOptionsData, err := os.ReadFile(eventOptionsPath)
	if err != nil {
		logger.LogError("Failed to load event options: %v", err)
		return nil
	}

	var allEventOptions map[string]interface{}
	if err := json.Unmarshal(eventOptionsData, &allEventOptions); err != nil {
		logger.LogError("Failed to parse event options: %v", err)
		return nil
	}

	if eventOptions, ok := allEventOptions[eventName].(map[string]interface{}); ok {
		return eventOptions
	}

	return nil
}

// success pages

func handleEventSuccessPage(w http.ResponseWriter, r *http.Request, formID, token string, isAdminView bool, adminToken string) {
	// Admin check
	if isAdminView {
		referer := r.Header.Get("Referer")
		if !security.ValidateAdminToken(adminToken, true, referer) {
			logger.LogWarn("Invalid admin token access attempt to event success page from %s (referer: %s)", logger.GetClientIP(r), referer)
			http.Error(w, "Invalid admin access", http.StatusForbidden)
			return
		}
		logger.LogInfo("Valid admin token access to event success page for formID %s from %s", formID, logger.GetClientIP(r))
	}

	// Load the submission (needed for both admin and user flows)
	sub, err := data.GetEventByID(formID)
	if err != nil {
		logger.LogError("GetEventByID failed for %s: %v", formID, err)
		http.Error(w, "Order details not found", http.StatusNotFound)
		return
	}

	// Token validation for non-admin users
	if !isAdminView {
		if token == "" {
			logger.LogWarn("Event success page accessed without token from %s", logger.GetClientIP(r))
			showTokenExpiredPage(w, "event")
			return
		}

		// Always validate token format and ownership
		tokenInfo := security.GetTokenInfo(token)

		if tokenInfo == nil || tokenInfo.FormID != formID {
			// Fallback: For completed payments, check against database token
			if sub.PayPalStatus == "COMPLETED" && sub.AccessToken == token {
				logger.LogInfo("Using database token validation for completed payment %s (server restart recovery)", formID)
			} else {
				logger.LogWarn("Invalid or mismatched token for formID %s from %s", formID, logger.GetClientIP(r))
				http.Error(w, "Invalid access", http.StatusForbidden)
				return
			}
		} else {
			// Token found in memory - use normal flow
			// For completed payments, allow expired tokens but still require valid token
			if sub.PayPalStatus == "COMPLETED" {
				logger.LogInfo("Allowing access to completed payment with potentially expired token for %s", formID)
			} else {
				// For incomplete payments, enforce normal token validation
				if !security.ValidateAccessToken(token, 15*time.Minute) {
					logger.LogWarn("Expired token for incomplete payment from %s for formID %s", logger.GetClientIP(r), formID)
					showTokenExpiredPage(w, "event")
					return
				}

				// Use the token (mark as used) for incomplete payments only
				if security.UseAccessToken(token) == nil {
					logger.LogWarn("Token already used for incomplete payment from %s for formID %s", logger.GetClientIP(r), formID)
					showTokenExpiredPage(w, "event")
					return
				}
			}
		}
	}

	// Continue with the rest of the function (remove the loadEventSuccess: label)
	// 3. Generate static order page if payment is completed and not admin view
	if !isAdminView && sub.PayPalStatus == "COMPLETED" && sub.OrderPageURL == "" {
		logger.LogInfo("DEBUG: About to generate static page - FoodOrderID: '%s', HasFoodOrders: %v", sub.FoodOrderID, sub.HasFoodOrders)

		orderPagePath, err := generateStaticOrderPage(sub)
		if err != nil {
			logger.LogError("Failed to generate static order page for %s: %v", formID, err)
			// Don't fail the request, just log the error
		} else {
			// Update database with order page URL
			if err := data.UpdateEventOrderPageURL(formID, orderPagePath); err != nil {
				logger.LogError("Failed to update order page URL for %s: %v", formID, err)
			}
			sub.OrderPageURL = orderPagePath
		}

		// Send confirmation emails
		if err := sendEventConfirmationEmailIfNeeded(sub); err != nil {
			logger.LogError("Failed to send event confirmation email for %s: %v", formID, err)
		}
	}

	// 4. Parse event selections for display
	eventSelections, eventItemsDisplay, totalFromSelections := parseEventSelectionsForDisplay(sub.FoodChoicesJSON, sub.Event)

	// 5. Get student names for better display
	studentNames := make(map[string]string)
	for i, student := range sub.Students {
		studentNames[fmt.Sprintf("%d", i)] = student.Name
	}

	// Update student names in display items
	for i := range eventItemsDisplay {
		if !eventItemsDisplay[i].IsShared {
			// Extract student index from StudentName (e.g., "Student 0" -> "0")
			parts := strings.Split(eventItemsDisplay[i].StudentName, " ")
			if len(parts) > 1 {
				studentIndex := parts[1]
				if realName, exists := studentNames[studentIndex]; exists {
					eventItemsDisplay[i].StudentName = realName
				}
			}
		}
	}

	// 6. Prepare template data
	resp := struct {
		FormID              string
		FormattedID         string
		Event               string
		FullName            string
		FirstName           string
		LastName            string
		Email               string
		School              string
		StudentCount        int
		Students            []data.Student
		EventSelections     interface{}
		EventItemsDisplay   []EventItemDisplay
		CalculatedAmount    float64
		CoverFees           bool
		ProcessingFee       float64
		FoodOrderID         string
		OrderPageURL        string
		SubmittedAt         *time.Time
		PayPalOrderID       string
		PayPalStatus        string
		TotalFromSelections float64
		IsCompleted         bool
		IsAdminView         bool
		Year                int
	}{
		FormID:              sub.FormID,
		FormattedID:         formatReceiptID(sub.FormID),
		Event:               formatDisplayName(sub.Event),
		FullName:            sub.FullName,
		FirstName:           sub.FirstName,
		LastName:            sub.LastName,
		Email:               sub.Email,
		School:              formatDisplayName(sub.School),
		StudentCount:        sub.StudentCount,
		Students:            sub.Students,
		EventSelections:     eventSelections,
		EventItemsDisplay:   eventItemsDisplay,
		CalculatedAmount:    sub.CalculatedAmount,
		CoverFees:           sub.CoverFees,
		ProcessingFee:       calculateProcessingFee(sub.CalculatedAmount, sub.CoverFees),
		FoodOrderID:         sub.FoodOrderID,
		OrderPageURL:        sub.OrderPageURL,
		SubmittedAt:         sub.SubmittedAt,
		PayPalOrderID:       sub.PayPalOrderID,
		PayPalStatus:        sub.PayPalStatus,
		TotalFromSelections: totalFromSelections,
		IsCompleted:         sub.PayPalStatus == "COMPLETED",
		IsAdminView:         isAdminView,
		Year:                time.Now().Year(),
	}

	// 7. Render the event success template
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := eventSuccessTmpl.Execute(w, resp); err != nil {
		logger.LogError("Failed to render event success template: %v", err)
		http.Error(w, "Error rendering page", http.StatusInternalServerError)
	}
}

// special event flow: create the static page for links to food orders
// generateStaticOrderPage creates a static HTML page for the event order
func generateStaticOrderPage(sub *data.EventSubmission) (string, error) {
	logger.LogInfo("DEBUG: generateStaticOrderPage called for form %s", sub.FormID)
	logger.LogInfo("DEBUG: FoodOrderID: '%s'", sub.FoodOrderID)
	logger.LogInfo("DEBUG: HasFoodOrders: %v", sub.HasFoodOrders)
	logger.LogInfo("DEBUG: Event: '%s'", sub.Event)

	// ... rest of existing function
	// Get base path from environment
	basePathEnv := config.GetEnvBasedSetting("EVENT_ORDERS_PATH")
	if basePathEnv == "" {
		basePathEnv = "/home/public/events"
	}

	// Create directory structure: /base/YEAR/event_name/
	year := time.Now().Year()
	eventName := strings.ReplaceAll(sub.Event, " ", "-")
	dirPath := filepath.Join(basePathEnv, strconv.Itoa(year), eventName)

	// Create directory if it doesn't exist
	if err := os.MkdirAll(dirPath, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}

	// Generate filename using food order ID
	filename := fmt.Sprintf("%s.html", sub.FoodOrderID)
	filePath := filepath.Join(dirPath, filename)

	// Parse event selections for display (using our new function)
	_, eventItemsDisplay, totalFromSelections := parseEventSelectionsForDisplay(sub.FoodChoicesJSON, sub.Event)

	// Get student names for better display
	studentNames := make(map[string]string)
	for i, student := range sub.Students {
		studentNames[fmt.Sprintf("%d", i)] = student.Name
	}

	// Update student names in display items
	for i := range eventItemsDisplay {
		if !eventItemsDisplay[i].IsShared {
			// Extract student index from StudentName (e.g., "Student 0" -> "0")
			parts := strings.Split(eventItemsDisplay[i].StudentName, " ")
			if len(parts) > 1 {
				studentIndex := parts[1]
				if realName, exists := studentNames[studentIndex]; exists {
					eventItemsDisplay[i].StudentName = realName
				}
			}
		}
	}

	// Create the HTML content with the new structure
	tmpl := `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.Event}} Order - {{.FoodOrderID}}</title>
    <link rel="stylesheet" href="/static/css/foodorders.css">
</head>
<body>
    <header>
        <h1>{{.Event}} - Food Order</h1>
        <p>Order ID: <strong>{{.FoodOrderID}}</strong></p>
        <p>For: <strong>{{.FullName}}</strong></p>
    </header>
    
    <main>
        <section aria-labelledby="registration-heading">
            <h2 id="registration-heading">Registration Details</h2>
            <dl>
                <dt>Parent/Guardian:</dt>
                <dd>{{.FullName}}</dd>
                
                <dt>Email:</dt>
                <dd><a href="mailto:{{.Email}}">{{.Email}}</a></dd>
                
                <dt>School:</dt>
                <dd>{{.School}}</dd>
                
                <dt>Payment Date:</dt>
                <dd><time datetime="{{.SubmittedAt.Format "2006-01-02T15:04:05Z07:00"}}">{{.SubmittedAt.Format "January 2, 2006 at 3:04 PM"}}</time></dd>
                
                <dt>Payment ID:</dt>
                <dd>{{.PayPalOrderID}}</dd>
            </dl>
        </section>
        
        <section aria-labelledby="students-heading">
            <h2 id="students-heading">Registered Students</h2>
            <ul>
                {{range .Students}}
                <li>{{.Name}} - Grade {{.Grade}}</li>
                {{end}}
            </ul>
        </section>
        
        {{if .EventItemsDisplay}}
        <section aria-labelledby="selections-heading">
            <h2 id="selections-heading">Selected Options</h2>
            
            {{/* Group and display per-student options */}}
            {{$hasPerStudentItems := false}}
            {{$hasSharedItems := false}}
            
            {{range .EventItemsDisplay}}
              {{if .IsShared}}
                {{$hasSharedItems = true}}
              {{else}}
                {{$hasPerStudentItems = true}}
              {{end}}
            {{end}}
            
            {{if $hasPerStudentItems}}
            <section aria-labelledby="per-student-heading">
                <h3 id="per-student-heading">Per-Student Options</h3>
                <table>
                    <thead>
                        <tr>
                            <th scope="col">Student & Option</th>
                            <th scope="col">Amount</th>
                        </tr>
                    </thead>
                    <tbody>
                        {{range .EventItemsDisplay}}
                          {{if not .IsShared}}
                          <tr>
                              <td><strong>{{.StudentName}}</strong> - {{.ItemLabel}}</td>
                              <td>${{printf "%.2f" .TotalPrice}}</td>
                          </tr>
                          {{end}}
                        {{end}}
                    </tbody>
                </table>
            </section>
            {{end}}
            
            {{if $hasSharedItems}}
            <section aria-labelledby="shared-heading">
                <h3 id="shared-heading">Additional Options</h3>
                <table>
                    <thead>
                        <tr>
                            <th scope="col">Option</th>
                            <th scope="col">Amount</th>
                        </tr>
                    </thead>
                    <tbody>
                        {{range .EventItemsDisplay}}
                          {{if .IsShared}}
                          <tr>
                              <td>{{.ItemLabel}} {{if gt .Quantity 1}}(×{{.Quantity}}){{end}}</td>
                              <td>${{printf "%.2f" .TotalPrice}}</td>
                          </tr>
                          {{end}}
                        {{end}}
                    </tbody>
                </table>
            </section>
            {{end}}
        </section>
        {{end}}
        
        <aside class="total-summary" aria-labelledby="total-heading">
            <h2 id="total-heading">Total Amount</h2>
            <p class="total-amount">${{printf "%.2f" .CalculatedAmount}}</p>
        </aside>
    </main>
    
    <footer>
        <h2>Thank you for your registration!</h2>
        <p>Please print or save this page for your records.</p>
        <p>If you have questions, contact us at <a href="mailto:info@hebstrings.org">info@hebstrings.org</a></p>
    </footer>
</body>
</html>`

	// Parse and execute template
	t, err := template.New("orderPage").Funcs(template.FuncMap{
		"formatCurrency": func(amount float64) string {
			return fmt.Sprintf("$%.2f", amount)
		},
	}).Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	// Create the file
	file, err := os.Create(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	// Execute template to file
	templateData := struct {
		*data.EventSubmission
		Event               string
		EventItemsDisplay   []EventItemDisplay
		TotalFromSelections float64
	}{
		EventSubmission:     sub,
		Event:               formatDisplayName(sub.Event),
		EventItemsDisplay:   eventItemsDisplay,
		TotalFromSelections: totalFromSelections,
	}

	if err := t.Execute(file, templateData); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	// Return the relative URL path
	publicURL := fmt.Sprintf("/events/%d/%s/%s", year, eventName, filename)
	return publicURL, nil
}

// emails and other notifications

// sendEventConfirmationEmailIfNeeded sends confirmation email for events
func sendEventConfirmationEmailIfNeeded(sub *data.EventSubmission) error {
	// For now, we'll use a simple approach - you can enhance this later
	config := email.LoadEmailConfig()

	subject := fmt.Sprintf("Event Registration Confirmation - %s", formatDisplayName(sub.Event))

	orderLink := ""
	if sub.OrderPageURL != "" {
		baseURL := os.Getenv("PUBLIC_BASE_URL")
		if baseURL == "" {
			baseURL = "https://suzuki.nfshost.com"
		}
		orderLink = fmt.Sprintf("%s%s", baseURL, sub.OrderPageURL)
	}

	body := fmt.Sprintf(`Dear %s,

Thank you for registering for %s!

Event Details:
- Order ID: %s
- School: %s
- Students Registered: %d
- Total Amount: $%.2f
- Payment ID: %s

View your order details: %s

If you have any questions, please contact us.

Best regards,
The Event Team`,
		sub.FirstName,
		formatDisplayName(sub.Event),
		sub.FoodOrderID,
		formatDisplayName(sub.School),
		sub.StudentCount,
		sub.CalculatedAmount,
		sub.PayPalOrderID,
		orderLink,
	)

	return email.SendMail(sub.Email, config.ConfirmationSender, subject, body)
}
