// internal/data/data.go
package data

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sbcbackend/internal/config"
	"sbcbackend/internal/logger"
)

// GetCurrentTimeInZone returns the current time formatted in the specified time zone.
func GetCurrentTimeInZone(loc *time.Location) string {
	currentTime := time.Now().In(loc)
	return currentTime.Format("2006-01-02 15:04:05 MST")
}

// GetFilePathForForm returns the file path for a given form ID and year.
func GetFilePathForForm(formID string, year int) string {
	// Form ID format is now: "membership-2025-04-27_11-15-00-0H5tHQ"
	// So, we need to use the formID directly without appending `.json` inside this function.

	// Return the full path by using the year, formID, and .json extension.
	return fmt.Sprintf("%s/%d/%s.json", config.GetFormsDataDirectory(), year, formID)
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

// loadValidNames loads valid names (memberships/products) from a JSON file. used to display JSON inventory lists on checkout pages.
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

// SaveFormDataToJSON saves form data into a categorized JSON file based on formID
func SaveFormDataToJSON(formData map[string]string, formType string) error {
	formID := formData["formID"]
	if formID == "" {
		return fmt.Errorf("formID is missing, unable to save form")
	}

	// Extract form type from formID
	parts := strings.SplitN(formID, "-", 2)
	formType = parts[0]

	// Validate known form types
	validTypes := map[string]string{
		"membership": "memberships",
		"payment":    "payments",
		"event":      "events",
	}
	category, ok := validTypes[formType]
	if !ok {
		category = "others" // fallback if unknown
	}

	year := time.Now().Format("2006")
	baseDataDir := config.DataDirectory()
	dirPath := filepath.Join(baseDataDir, category, year)

	// Make sure directory exists
	if err := os.MkdirAll(dirPath, 0755); err != nil {
		logger.LogError("Failed to create directory %s: %v", dirPath, err)
		return fmt.Errorf("failed to create directory %s: %v", dirPath, err)
	}

	// Final filename: formID.json
	filename := fmt.Sprintf("%s.json", formID)
	filePath := filepath.Join(dirPath, filename)

	// Save the form
	file, err := os.Create(filePath)
	if err != nil {
		logger.LogError("Failed to create file %s: %v", filePath, err)
		return fmt.Errorf("failed to create file: %v", err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(formData); err != nil {
		logger.LogError("Failed to write data to file %s: %v", filePath, err)
		return fmt.Errorf("failed to encode form data: %v", err)
	}

	logger.LogInfo("Form saved successfully: %s", filePath)
	return nil
}

// WriteFileSafely writes JSON data to a file, ensuring safe writing and file handling.
// It handles retries, temporary file writing, and renaming for atomic writes.
func WriteFileSafely(filePath string, data interface{}) error {
	// Create a temporary file first
	tmpPath := filePath + ".tmp"
	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		logger.LogError("Failed to create temporary file %s: %v", tmpPath, err)
		return fmt.Errorf("failed to create temporary file: %w", err)
	}
	defer tmpFile.Close()

	// Encode the data to the temporary file
	encoder := json.NewEncoder(tmpFile)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(data); err != nil {
		logger.LogError("Failed to encode data to temporary file %s: %v", tmpPath, err)
		return fmt.Errorf("failed to encode data: %w", err)
	}

	// Try renaming the temporary file to the final file path
	for i := 0; i < 3; i++ {
		err := os.Rename(tmpPath, filePath)
		if err == nil {
			logger.LogInfo("Successfully wrote file %s", filePath)
			return nil
		}
		if strings.Contains(err.Error(), "file exists") {
			// Handle scenario where the file might already exist, retry
			time.Sleep(500 * time.Millisecond)
			continue
		}
		logger.LogError("Failed to rename temporary file to %s: %v", filePath, err)
		return fmt.Errorf("failed to rename file: %w", err)
	}

	return fmt.Errorf("failed to rename temporary file after multiple attempts: %w", err)
}
