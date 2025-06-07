// main.go
package main

import (
	"context"
	"log"
	_ "modernc.org/sqlite"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"sbcbackend/internal/cleanup"
	"sbcbackend/internal/config"
	"sbcbackend/internal/data"
	"sbcbackend/internal/email"
	"sbcbackend/internal/form"
	"sbcbackend/internal/info"
	"sbcbackend/internal/inventory"
	"sbcbackend/internal/logger"
	"sbcbackend/internal/middleware"
	"sbcbackend/internal/order"
	"sbcbackend/internal/payment"
	"sbcbackend/internal/security"
	"sbcbackend/internal/webhook"
)

type App struct {
	addr          string
	mux           *http.ServeMux
	connections   sync.WaitGroup
	totalRequests int64
}

func init() {
	loc, err := time.LoadLocation("America/Chicago")
	if err == nil {
		time.Local = loc // This affects the standard log package
	}
}

// Global inventory service for handlers to access
var globalInventoryService *inventory.Service

func main() {
	// Step 1: Setup configuration first
	config.LoadEnv()
	config.ConfigurePaths()

	// Step 2: Setup logging
	loggerConfig := config.LoggerConfig()
	if err := logger.SetupLogger(loggerConfig); err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}

	logger.LogInfo("Environment and paths loaded. Logger ready.")

	// Step 3: Initialize SQLite database
	dbPath := "./booster/data/booster.db"
	if err := data.InitDB(dbPath); err != nil {
		logger.LogFatal("Failed to initialize SQLite DB: %v", err)
	}
	defer func() {
		if err := data.CloseDB(); err != nil {
			logger.LogError("Error closing DB: %v", err)
		}
	}()
	if err := data.CreateTables(); err != nil {
		logger.LogFatal("Failed to create tables: %v", err)
	}

	// Step 4: Load PayPal configuration
	if err := config.LoadPayPalConfig(); err != nil {
		logger.LogFatal("Failed to load PayPal config: %v", err)
	}

	// Step 4b: log .env setting
	config.LogCurrentEnvironment()

	// Step 4c: Initialize Inventory Service
	inventoryService := inventory.NewService()

	// Check if we should use unified inventory.json or legacy files
	inventoryPath := config.GetEnvBasedSetting("INVENTORY_JSON_PATH")
	if inventoryPath != "" {
		// Use unified inventory.json
		logger.LogInfo("Loading unified inventory from: %s", inventoryPath)
		err := inventoryService.LoadInventory(inventoryPath)
		if err != nil {
			logger.LogFatal("Failed to load unified inventory: %v", err)
		}
	} else {
		// Fallback to legacy files
		logger.LogInfo("Loading legacy inventory files")
		membershipsPath := config.GetEnvBasedSetting("MEMBERSHIPS_JSON_PATH")
		if membershipsPath == "" {
			membershipsPath = "/home/public/static/memberships.json"
		}
		productsPath := config.GetEnvBasedSetting("PRODUCTS_JSON_PATH")
		if productsPath == "" {
			productsPath = "/home/public/static/products.json"
		}
		feesPath := config.GetEnvBasedSetting("FEES_JSON_PATH")
		if feesPath == "" {
			feesPath = "/home/public/static/fees.json"
		}
		eventsPath := config.GetEnvBasedSetting("EVENT_OPTIONS_PATH")
		if eventsPath == "" {
			eventsPath = "/home/public/static/event-purchases.json"
		}

		err := inventoryService.LoadInventory(membershipsPath, productsPath, feesPath, eventsPath)
		if err != nil {
			logger.LogFatal("Failed to load legacy inventory: %v", err)
		}
	}

	logger.LogInfo("Inventory service initialized with %v cache", inventoryService.CacheAge())

	// Store globally for handlers to access
	globalInventoryService = inventoryService

	payment.SetInventoryService(inventoryService)
	order.SetInventoryService(inventoryService)

	// Step 5: Setup app
	app := &App{
		addr: serverAddress(),
		mux:  routes(),
	}

	// Step 6: Start background tasks (if any remain, like token cleanup)
	go security.CleanExpiredTokens()
	cleanup.StartCleanupRoutine()
	// go data.StartMembershipAggregator() // REMOVE if now obsolete

	// Step 7: Run server
	app.Run()
}

// serverAddress builds the server address from environment variables
func serverAddress() string {
	host := os.Getenv("SERVER_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	port := os.Getenv("SERVER_PORT")
	if port == "" {
		port = "5051"
	}
	return host + ":" + port
}

// routes sets up all API routes with appropriate middleware
func routes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	apiMux := http.NewServeMux()

	// Protected endpoints - require full API middleware (token validation, rate limiting, etc.)
	apiMux.Handle("/order-details", middleware.APIMiddleware(order.GetPaymentDetailsHandler))
	apiMux.Handle("/save-event-payment", middleware.APIMiddleware(payment.SaveEventPaymentHandler))
	apiMux.Handle("/save-membership-payment", middleware.APIMiddleware(payment.SaveMembershipPaymentHandler))
	apiMux.Handle("/create-order", middleware.APIMiddleware(payment.CreatePayPalOrderHandler))
	apiMux.Handle("/capture-order", middleware.APIMiddleware(payment.CapturePayPalOrderHandler))
	apiMux.Handle("/success", middleware.APIMiddleware(order.GetSuccessPageHandler))
	apiMux.Handle("/token-info", middleware.APIMiddleware(security.AccessTokenInfoHandler))

	// Special endpoints - keep existing behavior
	apiMux.HandleFunc("/submit-form", form.SubmitFormHandler)          // Has its own validation
	apiMux.HandleFunc("/paypal-webhook", webhook.PayPalWebhookHandler) // External webhook
	apiMux.HandleFunc("/csrf-token", security.CSRFTokenHandler)        // Public endpoint

	// Test endpoint with basic middleware (no token required)
	apiMux.Handle("/test-email", middleware.RequestID(middleware.Logging(func(w http.ResponseWriter, r *http.Request) {
		if err := email.TestEmailFunctionality(); err != nil {
			middleware.WriteAPIError(w, r, http.StatusInternalServerError, "email_test_failed",
				"Email test failed", err.Error())
			return
		}
		middleware.WriteAPISuccess(w, r, map[string]string{
			"message": "✅ Email tests completed successfully! Check your application logs to see the mock emails.",
		})
	})))

	mux.Handle("/api/", http.StripPrefix("/api", apiMux))
	mux.HandleFunc("/info", info.InfoPageHandler)

	return mux
}

// Run starts the HTTP server

func (a *App) Run() {
	server := &http.Server{
		Addr:         a.addr,
		Handler:      a.Handler(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Channel to listen for shutdown signals
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	// Start server in a separate goroutine
	go func() {
		logger.LogInfo("Starting server on %s", a.addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.LogFatal("Server failed: %v", err)
		}
	}()

	// Wait for a shutdown signal
	<-stop
	logger.LogInfo("Shutdown signal received")

	// Create context with timeout for shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Shutdown the server gracefully
	if err := server.Shutdown(ctx); err != nil {
		logger.LogError("Server shutdown error: %v", err)
	} else {
		logger.LogInfo("Server shut down gracefully")
	}

	// Wait for active connections to finish
	logger.LogInfo("Shutdown signal received")
	logger.LogInfo("Waiting for active connections to finish...")
	a.connections.Wait()
	logger.LogInfo("All connections closed. Total requests handled: %d", atomic.LoadInt64(&a.totalRequests))
	logger.LogInfo("Server shut down gracefully")
}

// Handler assembles all middleware around the main mux
func (a *App) Handler() http.Handler {
	var handler http.Handler = a.mux
	handler = security.AddCORSHeaders(handler)
	handler = withCustom404(handler)
	handler = a.trackConnections(handler)
	handler = logRequests(handler)
	handler = withTimeout(handler, 15*time.Second)

	return handler
}

// Middleware: timeout handler
func withTimeout(h http.Handler, timeout time.Duration) http.Handler {
	return http.TimeoutHandler(h, timeout, "Request timed out")
}

// Middleware: log requests
func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		h.ServeHTTP(w, r)

		duration := time.Since(start)
		logger.LogInfo("%s %s took %v", r.Method, r.URL.Path, duration)
	})
}

// Middleware: track active connections and total requests
func (a *App) trackConnections(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.connections.Add(1)
		atomic.AddInt64(&a.totalRequests, 1)
		defer a.connections.Done()

		h.ServeHTTP(w, r)
	})
}

// Middleware: custom 404 page
func withCustom404(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Use a custom response writer to capture the status code
		crw := &captureResponseWriter{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
		}

		// Let the handler chain process the request
		h.ServeHTTP(crw, r)

		// Check if a 404 was encountered
		if crw.statusCode == http.StatusNotFound {
			logger.LogInfo("404 not found: %s", r.URL.Path)

			// Reset headers to avoid conflicts
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`
				<html><body>
					<h1>404 - Page Not Found</h1>
					<p>Sorry, the page you requested was not found.</p>
					<a href="/membership.html">Return to Membership Page</a>
				</body></html>
			`))
		}
	})
}

// captureResponseWriter tracks status code without writing to the underlying response writer
type captureResponseWriter struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

func (crw *captureResponseWriter) WriteHeader(code int) {
	if !crw.written {
		crw.statusCode = code
		crw.written = true
		crw.ResponseWriter.WriteHeader(code)
	}
}

func (crw *captureResponseWriter) Write(b []byte) (int, error) {
	if !crw.written {
		crw.WriteHeader(http.StatusOK)
	}
	return crw.ResponseWriter.Write(b)
}
