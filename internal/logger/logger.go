// internal/logger/logger.go
package logger

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Logger configuration
type Config struct {
	LogsDirectory string
	LogFileFormat string
	TimeZone      string
}

var (
	initialized  int32 // 0 = not initialized, 1 = initialized
	logger       *log.Logger
	loggerOutput io.Writer
	timeZone     *time.Location
	logFilePath  string
	mu           sync.Mutex // protect against concurrent initialization
)

// SetupLogger initializes the logger with file and console output.
func SetupLogger(config Config) error {
	mu.Lock()
	defer mu.Unlock()

	if atomic.LoadInt32(&initialized) == 1 {
		return fmt.Errorf("logger already initialized")
	}

	if config.TimeZone == "" {
		config.TimeZone = "America/Chicago"
	}

	loc, err := time.LoadLocation(config.TimeZone)
	if err != nil {
		fallbackLogFatal("Failed to load time zone '%s': %v", config.TimeZone, err)
	}
	timeZone = loc

	// Log working directory for early diagnosis
	if wd, err := os.Getwd(); err == nil {
		fmt.Printf("[INFO] Current working directory: %s\n", wd)
	} else {
		fmt.Printf("[WARN] Failed to get working directory: %v\n", err)
	}

	if err := os.MkdirAll(config.LogsDirectory, 0775); err != nil {
		fallbackLogFatal("Failed to create logs directory '%s': %v", config.LogsDirectory, err)
	}

	currentTime := time.Now().In(loc)
	logFileName := fmt.Sprintf(config.LogFileFormat, currentTime.Format("2006-01-02"))

	// Respect whether LogFileFormat is an absolute path or not
	if filepath.IsAbs(logFileName) {
		logFilePath = logFileName
	} else {
		logFilePath = filepath.Join(config.LogsDirectory, logFileName)
	}

	logFile, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0664)
	if err != nil {
		fallbackLogFatal("Failed to open log file '%s': %v", logFilePath, err)
	}

	multi := io.MultiWriter(os.Stdout, logFile)
	loggerOutput = multi
	logger = log.New(multi, "", log.Ldate|log.Ltime)

	atomic.StoreInt32(&initialized, 1)
	LogInfo("Logger initialized, writing to %s", logFilePath)
	return nil
}

func GetLogFilePath() string {
	return logFilePath
}

func IsInitialized() bool {
	return atomic.LoadInt32(&initialized) == 1
}

func LogMessage(level string, message string, v ...interface{}) {
	if !IsInitialized() {
		log.Printf("[%s] %s", level, fmt.Sprintf(message, v...))
		return
	}

	_, file, line, _ := runtime.Caller(2)
	fileName := filepath.Base(file)
	formattedMsg := fmt.Sprintf(message, v...)
	timestamp := time.Now().In(timeZone).Format("2006-01-02 15:04:05 MST")

	full := fmt.Sprintf("[%s] %s %s:%d - %s", level, timestamp, fileName, line, formattedMsg)
	logger.Println(full)
}

func LogInfo(message string, v ...interface{})  { LogMessage("INFO", message, v...) }
func LogWarn(message string, v ...interface{})  { LogMessage("WARN", message, v...) }
func LogError(message string, v ...interface{}) { LogMessage("ERROR", message, v...) }
func LogFatal(message string, v ...interface{}) {
	LogMessage("FATAL", message, v...)
	os.Exit(1)
}

func LogHTTPRequest(r *http.Request) {
	clientIP := GetClientIP(r)
	LogInfo("HTTP %s %s from %s", r.Method, r.URL.Path, clientIP)
}

func LogHTTPError(r *http.Request, status int, err error) {
	clientIP := GetClientIP(r)
	LogError("HTTP %d error for %s %s from %s: %v", status, r.Method, r.URL.Path, clientIP, err)
}

func GetClientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		return strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}
	if real := r.Header.Get("X-Real-IP"); real != "" {
		return real
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// fallbackLogFatal ensures logger setup issues still show in stdout and kill the app
func fallbackLogFatal(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	fmt.Fprintf(os.Stderr, "[FATAL] %s\n", msg)
	os.Exit(1)
}
