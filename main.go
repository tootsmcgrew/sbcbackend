// main.go
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"sbcbackend/internal/config"
	"sbcbackend/internal/form"
	"sbcbackend/internal/logger"
	"sbcbackend/internal/payment"
	"sbcbackend/internal/security"
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

func main() {
	// Step 1: Setup configuration first
	config.LoadEnv()
	config.ConfigurePaths()

	// Step 2: Setup logging
	loggerConfig := config.LoggerConfig()
	if err := logger.SetupLogger(loggerConfig); err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}

	// Only NOW is logging safe to use!
	logger.LogInfo("Environment and paths loaded. Logger ready.")

	// Step 3: Load PayPal configuration
	if err := config.LoadPayPalConfig(); err != nil {
		logger.LogFatal("Failed to load PayPal config: %v", err)
	}

	// *** Step 4: Preload pending payments ***
	if err := payment.PreloadPendingPayments(); err != nil {
		logger.LogFatal("Failed to preload pending payments: %v", err)
	}
	logger.LogInfo("Pending payments preloaded successfully.")

	// Step 4b: log .env setting
	config.LogCurrentEnvironment()

	// Step 5: Setup app
	app := &App{
		addr: serverAddress(),
		mux:  routes(),
	}

	// Step 6: Start background tasks
	go security.CleanExpiredTokens()

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

// routes sets up all API routes
func routes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/order-details", payment.GetPaymentDetailsHandler)
	apiMux.HandleFunc("/submit-form", form.SubmitFormHandler)
	apiMux.HandleFunc("/paypal-webhook", payment.PayPalWebhookHandler)
	apiMux.HandleFunc("/csrf-token", security.CSRFTokenHandler)
	apiMux.HandleFunc("/save-payment-data", payment.SavePaymentDataHandler)
	apiMux.HandleFunc("/create-order", payment.CreatePayPalOrderHandler)
	apiMux.HandleFunc("/capture-order", payment.CapturePayPalOrderHandler)

	mux.Handle("/api/", http.StripPrefix("/api", apiMux))

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
