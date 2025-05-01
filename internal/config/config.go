// internal/config/config.go
package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	"sbcbackend/internal/logger"
)

// Variables available everywhere
var (
	clientID, clientSecret, apiBase string
	baseDir                         string
	dataDirectory                   string
	logsDirectory                   string
	webhookDirectory                string

	// Data file paths - exported
	ReconciliationFile = "reconciliation_%d.json"
	MembershipFile     = "membership_%d.json"
	LogFileFormat      string
	ReconciliationDir  string
	MembershipDir      string
	AllowedOrigin      string // For CORS
	RedirectBaseURL    string
	PayPalWebhookID    string
	formsDataDirectory string
)

//
// --- Utility Helpers ---
//

// Helper: get a setting based on ENVIRONMENT (dev or prod)
func GetEnvBasedSetting(base string) string {
	env := os.Getenv("ENVIRONMENT")
	if env == "" {
		env = "dev"
	}
	return os.Getenv(fmt.Sprintf("%s_%s", base, strings.ToUpper(env)))
}

// Helper: log which environment is running
func LogCurrentEnvironment() {
	env := os.Getenv("ENVIRONMENT")
	if env == "" {
		env = "dev"
	}

	if env == "dev" {
		logger.LogInfo("Running in development environment")
	} else {
		logger.LogInfo("Running in production environment")
	}
}

//
// --- Loaders ---
//

// LoadEnv reads .env file
func LoadEnv() {
	wd, err := os.Getwd()
	if err != nil {
		log.Printf("Could not determine working directory: %v", err)
	} else {
		log.Printf("Current working directory: %s", wd)
	}

	err = godotenv.Load(".env")
	if err != nil {
		log.Printf("No .env file found in %s. Using system environment variables.", wd)
	} else {
		log.Printf("Loaded environment variables from .env file in %s", wd)
	}
}

// LoggerConfig returns a logger.Config struct populated from environment
func LoggerConfig() logger.Config {
	logDir := GetEnvBasedSetting("LOGS_DIRECTORY")
	if logDir == "" {
		logDir = "./logs"
	}

	logFormat := GetEnvBasedSetting("LOG_FILE_FORMAT")
	if logFormat == "" {
		logFormat = "./logs/server_%s.log"
	}

	timezone := os.Getenv("TIME_ZONE")
	if timezone == "" {
		timezone = "Local"
	}

	return logger.Config{
		LogsDirectory: logDir,
		LogFileFormat: logFormat,
		TimeZone:      timezone,
	}
}

// ConfigurePaths sets up folders and paths
func ConfigurePaths() {
	wd, err := os.Getwd()
	if err != nil {
		logger.LogFatal("Failed to get working directory: %v", err)
	}
	baseDir = wd

	dataDir := GetEnvBasedSetting("DATA_DIRECTORY")
	if dataDir != "" {
		dataDirectory = dataDir
	} else {
		dataDirectory = filepath.Join(baseDir, "../data")
	}

	logsDir := GetEnvBasedSetting("LOGS_DIRECTORY")
	if logsDir != "" {
		logsDirectory = logsDir
	} else {
		logsDirectory = filepath.Join(baseDir, "../booster/data/memberships")
	}

	webhookDir := GetEnvBasedSetting("WEBHOOK_DIRECTORY")
	if webhookDir != "" {
		webhookDirectory = webhookDir
	} else {
		webhookDirectory = filepath.Join(baseDir, "../webhook")
	}

	formsDir := GetEnvBasedSetting("FORMS_DATA_DIRECTORY")
	if formsDir != "" {
		formsDataDirectory = formsDir
	} else {
		formsDataDirectory = filepath.Join(baseDir, "../forms")
	}

	// Set derived paths
	ReconciliationDir = filepath.Join(dataDirectory, "%d")
	MembershipDir = filepath.Join(dataDirectory, "%d")
	LogFileFormat = filepath.Join(logsDirectory, "server_%s.log")
}

// LoadPayPalConfig sets up PayPal info
func LoadPayPalConfig() error {
	clientID = os.Getenv("PAYPAL_CLIENT_ID")
	clientSecret = os.Getenv("PAYPAL_CLIENT_SECRET")

	if clientID == "" || clientSecret == "" {
		return fmt.Errorf("PayPal credentials are missing or incomplete")
	}

	mode := os.Getenv("PAYPAL_MODE")
	if mode == "live" {
		apiBase = "https://api.paypal.com"
		logger.LogInfo("Using PayPal Live environment")
	} else {
		apiBase = "https://api.sandbox.paypal.com"
		logger.LogInfo("Using PayPal Sandbox environment")
	}

	PayPalWebhookID = os.Getenv("PAYPAL_WEBHOOK_ID")
	if PayPalWebhookID == "" {
		logger.LogWarn("PAYPAL_WEBHOOK_ID is not set in environment")
	}

	return nil
}

// LoadCORSConfig loads CORS settings
func LoadCORSConfig() {
	AllowedOrigin = GetEnvBasedSetting("ALLOWED_ORIGIN")
	if AllowedOrigin == "" {
		AllowedOrigin = "*" // Allow all - be careful in prod
		logger.LogWarn("ALLOWED_ORIGIN not set, using '*' (allow all origins) - SECURITY RISK")
	} else {
		logger.LogInfo("Allowed Origin: %s", AllowedOrigin)
	}
}

// LoadRedirectConfig loads Redirect Base URL
func LoadRedirectConfig() {
	RedirectBaseURL = GetEnvBasedSetting("REDIRECT_BASE_URL")
	if RedirectBaseURL == "" {
		RedirectBaseURL = "http://hebstrings.local"
		logger.LogWarn("REDIRECT_BASE_URL not set, using default: %s", RedirectBaseURL)
	} else {
		logger.LogInfo("Redirect base URL: %s", RedirectBaseURL)
	}
}

//
// --- Getters (exported) ---
//

func DataDirectory() string {
	return dataDirectory
}

func WebhookDirectory() string {
	return webhookDirectory
}

func LogsDirectory() string {
	return logsDirectory
}

func APIBase() string {
	return apiBase
}

func ClientID() string {
	return clientID
}

func ClientSecret() string {
	return clientSecret
}

func GetFormsDataDirectory() string {
	return formsDataDirectory
}
