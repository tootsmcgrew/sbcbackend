// Replace your entire info.go with this simplified version

package info

import (
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"sbcbackend/internal/data"
	"sbcbackend/internal/logger"
	"sbcbackend/internal/security"
)

// Pre-parse template at startup (like your other endpoints)
var infoPageTmpl = template.Must(template.New("info.tmpl").Funcs(template.FuncMap{
	"joinStudentNames":  joinStudentNames,
	"dict":              dict,
	"formatCurrency":    formatCurrency,
	"formatDate":        formatDate,
	"formatDisplayName": formatDisplayName,
	"lower":             strings.ToLower,
}).ParseFiles("templates/info.tmpl"))

// Update InfoPageData struct to include events
type InfoPageData struct {
	Year               int
	Summary            data.MembershipSummary
	Entries            []data.MembershipSubmission
	InterestBySchool   []InterestSchoolRow
	Extras             data.MembershipExtras
	SortedFeePurchases []data.FeePurchase
	EventSummary       EventSummary                // Add this
	EventEntries       []data.EventSubmission      // Add this
	FundraiserSummary  data.FundraiserSummary      // Add this if you want
	FundraiserEntries  []data.FundraiserSubmission // Add this if you want
	AdminToken         string
	LastUpdated        time.Time
	ProcessingDuration string
}

type InterestSchoolRow struct {
	School   string
	Interest string
	Count    int
	People   []data.InterestPerson
}

// Add this struct for event data
type EventSummary struct {
	TotalEvents     int
	TotalStudents   int
	TotalRevenue    float64
	EventsByType    map[string]int
	EventsBySchool  map[string]int
	CompletedOrders int
	PendingOrders   int
}

// Update InfoPageHandler to include event data
func InfoPageHandler(w http.ResponseWriter, r *http.Request) {
	logger.LogHTTPRequest(r)
	startTime := time.Now()

	// Parse year parameter
	year, err := parseYear(r)
	if err != nil {
		logger.LogHTTPError(r, http.StatusBadRequest, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Get fresh data from database
	entries, err := data.GetMembershipsByYear(year)
	if err != nil {
		logger.LogHTTPError(r, http.StatusInternalServerError, err)
		http.Error(w, "Failed to load membership data", http.StatusInternalServerError)
		return
	}

	// Get event data
	eventEntries, err := data.GetEventsByYear(year)
	if err != nil {
		logger.LogError("Failed to load event data: %v", err)
		eventEntries = []data.EventSubmission{} // Continue with empty list
	}

	// Get fundraiser data
	fundraiserEntries, err := data.GetFundraisersByYear(year)
	if err != nil {
		logger.LogError("Failed to load fundraiser data: %v", err)
		fundraiserEntries = []data.FundraiserSubmission{}
	}

	// Compute summaries
	summary, extras := data.ComputeMembershipSummary(entries)
	eventSummary := computeEventSummary(eventEntries)
	fundraiserSummary := computeFundraiserSummary(fundraiserEntries)

	// Process interest data
	interestBySchool := processInterestData(extras)

	// Sort fee purchases by supporter name, then by fee name
	sortedFeePurchases := make([]data.FeePurchase, len(extras.FeePurchases))
	copy(sortedFeePurchases, extras.FeePurchases)
	sort.Slice(sortedFeePurchases, func(i, j int) bool {
		if sortedFeePurchases[i].FullName == sortedFeePurchases[j].FullName {
			return sortedFeePurchases[i].FeeName < sortedFeePurchases[j].FeeName
		}
		return sortedFeePurchases[i].FullName < sortedFeePurchases[j].FullName
	})

	// Generate temporary admin tokens for record access
	adminToken, err := security.GenerateAccessToken()
	if err != nil {
		logger.LogError("Failed to generate admin token: %v", err)
		adminToken = ""
	} else {
		security.StoreAccessToken(adminToken, "ADMIN", "admin_access")
		logger.LogInfo("Generated admin token for info page access")
	}

	// Prepare data for template
	pageData := InfoPageData{
		Year:               year,
		Summary:            summary,
		Entries:            entries,
		InterestBySchool:   interestBySchool,
		Extras:             extras,
		SortedFeePurchases: sortedFeePurchases,
		EventSummary:       eventSummary,
		EventEntries:       eventEntries,
		FundraiserSummary:  fundraiserSummary,
		FundraiserEntries:  fundraiserEntries,
		AdminToken:         adminToken,
		LastUpdated:        time.Now(),
		ProcessingDuration: time.Since(startTime).String(),
	}

	// Log processing
	logger.LogInfo("Info page generated for year %d in %v (memberships: %d, events: %d, fundraisers: %d)",
		year, time.Since(startTime), len(entries), len(eventEntries), len(fundraiserEntries))

	// Render template directly
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := infoPageTmpl.Execute(w, pageData); err != nil {
		logger.LogError("Failed to render info template: %v", err)
		http.Error(w, "Error rendering page", http.StatusInternalServerError)
		return
	}
}

// Helper functions (kept simple)
func parseYear(r *http.Request) (int, error) {
	yearStr := r.URL.Query().Get("year")
	if yearStr == "" {
		return time.Now().Year(), nil
	}

	year, err := strconv.Atoi(yearStr)
	if err != nil {
		return 0, fmt.Errorf("invalid year parameter")
	}

	currentYear := time.Now().Year()
	if year < currentYear-10 || year > currentYear+1 {
		return 0, fmt.Errorf("year must be between %d and %d", currentYear-10, currentYear+1)
	}

	return year, nil
}

func processInterestData(extras data.MembershipExtras) []InterestSchoolRow {
	// Sort people within each interest by school name
	for interest, people := range extras.Interests {
		sort.Slice(people, func(i, j int) bool {
			return people[i].School < people[j].School
		})
		extras.Interests[interest] = people
	}

	// Build school-interest mapping
	schoolInterestMap := make(map[string]map[string][]data.InterestPerson)
	for interest, people := range extras.Interests {
		for _, person := range people {
			if _, ok := schoolInterestMap[person.School]; !ok {
				schoolInterestMap[person.School] = make(map[string][]data.InterestPerson)
			}
			schoolInterestMap[person.School][interest] = append(
				schoolInterestMap[person.School][interest], person)
		}
	}

	// Convert to sorted slice
	var interestBySchool []InterestSchoolRow
	for school, interests := range schoolInterestMap {
		for interest, people := range interests {
			sort.Slice(people, func(i, j int) bool {
				return people[i].FullName < people[j].FullName
			})

			interestBySchool = append(interestBySchool, InterestSchoolRow{
				School:   school,
				Interest: interest,
				Count:    len(people),
				People:   people,
			})
		}
	}

	// Sort by school, then by interest
	sort.Slice(interestBySchool, func(i, j int) bool {
		if interestBySchool[i].School == interestBySchool[j].School {
			return interestBySchool[i].Interest < interestBySchool[j].Interest
		}
		return interestBySchool[i].School < interestBySchool[j].School
	})

	return interestBySchool
}

// Template helper functions (same as before)
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

func joinStudentNames(entry data.MembershipSubmission) string {
	names := make([]string, 0, len(entry.Students))
	for _, s := range entry.Students {
		if s.Name != "" {
			names = append(names, s.Name)
		}
	}
	return strings.Join(names, ", ")
}

func dict(values ...interface{}) map[string]interface{} {
	m := make(map[string]interface{})
	for i := 0; i < len(values); i += 2 {
		key, _ := values[i].(string)
		if i+1 < len(values) {
			m[key] = values[i+1]
		}
	}
	return m
}

func formatCurrency(amount float64) string {
	return fmt.Sprintf("$%.2f", amount)
}

func formatDate(t time.Time) string {
	return t.Format("Jan 2, 2006 3:04 PM")
}

// Add these helper functions
func computeEventSummary(events []data.EventSubmission) EventSummary {
	summary := EventSummary{
		TotalEvents:    len(events),
		EventsByType:   make(map[string]int),
		EventsBySchool: make(map[string]int),
	}

	for _, event := range events {
		summary.TotalStudents += event.StudentCount
		summary.TotalRevenue += event.CalculatedAmount

		// Count by event type
		summary.EventsByType[event.Event]++

		// Count by school
		summary.EventsBySchool[event.School]++

		// Count completed vs pending
		if event.PayPalStatus == "COMPLETED" {
			summary.CompletedOrders++
		} else {
			summary.PendingOrders++
		}
	}

	return summary
}

func computeFundraiserSummary(fundraisers []data.FundraiserSubmission) data.FundraiserSummary {
	// This is a placeholder - implement based on your needs
	summary := data.FundraiserSummary{
		TotalSubmissions: len(fundraisers),
		TotalAmount:      0,
		TotalStudents:    0,
	}

	for _, f := range fundraisers {
		summary.TotalAmount += f.CalculatedAmount
		summary.TotalStudents += f.StudentCount
	}

	return summary
}
