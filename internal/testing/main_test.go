package testing

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"sbcbackend/internal/data"
	"sbcbackend/internal/logger"
	"sbcbackend/internal/security"
)

var (
	// Test configuration flags
	runLoad     = flag.Bool("load", false, "Run load tests")
	runPayPal   = flag.Bool("paypal", false, "Run tests against real PayPal (requires credentials)")
	testTimeout = flag.Duration("timeout", 30*time.Second, "Test timeout duration")
	verbose     = flag.Bool("v", false, "Verbose test output")
	parallel    = flag.Int("parallel", 4, "Number of parallel test workers")
)

func TestMain(m *testing.M) {
	flag.Parse()

	// Configure logger for testing (logger doesn't have SetLogLevel, so we'll skip this)
	if *verbose {
		logger.LogInfo("Starting tests in verbose mode")
	}

	fmt.Println("🧪 Starting SBC Backend Test Suite")
	fmt.Println("===================================")

	// Set test timeout
	if *testTimeout > 0 {
		fmt.Printf("Test timeout: %v\n", *testTimeout)
	}

	// Configure parallel execution
	if *verbose {
		// testing.Verbose() takes no arguments, so we'll just print instead
		fmt.Println("Verbose mode enabled")
	}

	// Run tests
	exitCode := m.Run()

	fmt.Println("\n🏁 Test Suite Complete")
	fmt.Println("======================")

	os.Exit(exitCode)
}

// TestSystemIntegration runs comprehensive system tests
func TestSystemIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration tests in short mode")
	}

	suite := NewTestSuite(t)
	mockPayPal := NewMockPayPalService()
	defer mockPayPal.Close()

	t.Run("FullMembershipFlow", func(t *testing.T) {
		testFullMembershipFlow(t, suite, mockPayPal)
	})

	t.Run("FullEventFlow", func(t *testing.T) {
		testFullEventFlow(t, suite, mockPayPal)
	})

	t.Run("FullFundraiserFlow", func(t *testing.T) {
		testFullFundraiserFlow(t, suite, mockPayPal)
	})

	t.Run("ErrorRecovery", func(t *testing.T) {
		testErrorRecovery(t, suite, mockPayPal)
	})
}

func testFullMembershipFlow(t *testing.T, suite *TestSuite, mockPayPal *MockPayPalService) {
	// Simulate complete user journey
	start := time.Now()

	// 1. User submits membership form
	testData := suite.GenerateTestMembership("premium")
	submission := testData.ToMembershipSubmission()

	err := data.InsertMembership(submission)
	suite.AssertNoError(t, err)
	t.Logf("✓ Form submitted (FormID: %s)", testData.FormID)

	// 2. User configures payment options
	total, err := suite.Inventory.CalculateMembershipTotal(
		testData.Membership, testData.Addons, testData.Fees, testData.Donation, testData.CoverFees,
	)
	suite.AssertNoError(t, err)
	t.Logf("✓ Payment configured (Total: $%.2f)", total)

	// 3. PayPal order creation
	order, err := mockPayPal.CreateOrder(testData.FormID, fmt.Sprintf("%.2f", total))
	suite.AssertNoError(t, err)
	t.Logf("✓ PayPal order created (OrderID: %s)", order.ID)

	// 4. Database update
	now := time.Now()
	err = data.UpdateMembershipPayPalOrder(testData.FormID, order.ID, &now)
	suite.AssertNoError(t, err)

	// 5. PayPal capture
	err = mockPayPal.CaptureOrder(order.ID)
	suite.AssertNoError(t, err)
	t.Logf("✓ Payment captured")

	// 6. Final database update
	captureDetails := fmt.Sprintf(`{
		"id": "%s",
		"status": "COMPLETED",
		"purchase_units": [{"invoice_id": "%s"}]
	}`, order.ID, testData.FormID)

	err = data.UpdateMembershipPayPalCapture(testData.FormID, captureDetails, "COMPLETED", &now)
	suite.AssertNoError(t, err)

	// 7. Verify final state
	final, err := data.GetMembershipByID(testData.FormID)
	suite.AssertNoError(t, err)

	if final.PayPalStatus != "COMPLETED" {
		t.Errorf("Expected COMPLETED status, got %s", final.PayPalStatus)
	}

	duration := time.Since(start)
	t.Logf("✅ Full membership flow completed in %v", duration)
}

func testFullEventFlow(t *testing.T, suite *TestSuite, mockPayPal *MockPayPalService) {
	start := time.Now()

	// 1. Event registration
	testData := suite.GenerateTestEvent("multiple_students")
	submission := testData.ToEventSubmission()

	err := data.InsertEvent(submission)
	suite.AssertNoError(t, err)
	t.Logf("✓ Event registration submitted")

	// 2. Event options selection
	studentSelections := map[string]map[string]bool{
		"0": {"registration": true, "lunch": true},
		"1": {"registration": true, "lunch": true},
	}
	sharedSelections := map[string]int{"program": 1}

	total, err := suite.Inventory.CalculateEventTotal(
		testData.Event, studentSelections, sharedSelections, testData.CoverFees,
	)
	suite.AssertNoError(t, err)
	t.Logf("✓ Event options configured (Total: $%.2f)", total)

	// 3. Update event with selections
	selectionsJSON, _ := json.Marshal(map[string]interface{}{
		"student_selections": studentSelections,
		"shared_selections":  sharedSelections,
		"cover_fees":         testData.CoverFees,
		"has_food_orders":    true,
	})

	submission.FoodChoicesJSON = string(selectionsJSON)
	submission.CalculatedAmount = total
	submission.HasFoodOrders = true
	submission.FoodOrderID = "L-TEST123"

	err = data.UpdateEventPayment(submission)
	suite.AssertNoError(t, err)

	// Step 4: Test PayPal flow
	mockOrder, err := mockPayPal.CreateOrder(testData.FormID, fmt.Sprintf("%.2f", total))
	suite.AssertNoError(t, err)
	t.Logf("✓ PayPal order created for event")

	now := time.Now()
	err = data.UpdateEventPayPalOrder(testData.FormID, mockOrder.ID, &now)
	suite.AssertNoError(t, err)

	err = mockPayPal.CaptureOrder(mockOrder.ID)
	suite.AssertNoError(t, err)

	captureDetails := fmt.Sprintf(`{
		"id": "%s",
		"status": "COMPLETED",
		"purchase_units": [{"invoice_id": "%s"}]
	}`, mockOrder.ID, testData.FormID)

	err = data.UpdateEventPayPalCapture(testData.FormID, captureDetails, "COMPLETED", &now)
	suite.AssertNoError(t, err)

	// 5. Verify final state
	final, err := data.GetEventByID(testData.FormID)
	suite.AssertNoError(t, err)

	if final.PayPalStatus != "COMPLETED" {
		t.Errorf("Expected COMPLETED status, got %s", final.PayPalStatus)
	}
	if final.FoodOrderID != "L-TEST123" {
		t.Errorf("Food order ID mismatch: expected L-TEST123, got %s", final.FoodOrderID)
	}

	duration := time.Since(start)
	t.Logf("✅ Full event flow completed in %v", duration)
}

func testFullFundraiserFlow(t *testing.T, suite *TestSuite, mockPayPal *MockPayPalService) {
	start := time.Now()

	// 1. Fundraiser submission
	testData := suite.GenerateTestFundraiser("multiple_students", "cover_fees")
	submission := testData.ToFundraiserSubmission()

	err := data.InsertFundraiser(submission)
	suite.AssertNoError(t, err)
	t.Logf("✓ Fundraiser submitted")

	// 2. Process payment data (validation)
	err = data.ProcessFundraiserPayment(&submission)
	suite.AssertNoError(t, err)
	t.Logf("✓ Payment data processed (Total: $%.2f)", submission.CalculatedAmount)

	// 3. PayPal flow
	order, err := mockPayPal.CreateOrder(testData.FormID, fmt.Sprintf("%.2f", submission.CalculatedAmount))
	suite.AssertNoError(t, err)
	t.Logf("✓ PayPal order created for fundraiser")

	now := time.Now()
	err = data.UpdateFundraiserPayPalOrder(testData.FormID, order.ID, &now)
	suite.AssertNoError(t, err)

	err = mockPayPal.CaptureOrder(order.ID)
	suite.AssertNoError(t, err)

	captureDetails := fmt.Sprintf(`{
		"id": "%s",
		"status": "COMPLETED",
		"purchase_units": [{"invoice_id": "%s"}]
	}`, order.ID, testData.FormID)

	err = data.UpdateFundraiserPayPalCapture(testData.FormID, captureDetails, "COMPLETED", &now)
	suite.AssertNoError(t, err)

	// 4. Verify final state
	final, err := data.GetFundraiserByID(testData.FormID)
	suite.AssertNoError(t, err)

	if final.PayPalStatus != "COMPLETED" {
		t.Errorf("Expected COMPLETED status, got %s", final.PayPalStatus)
	}
	if len(final.DonationItems) != len(testData.DonationItems) {
		t.Errorf("Donation items mismatch")
	}

	duration := time.Since(start)
	t.Logf("✅ Full fundraiser flow completed in %v", duration)
}

func testErrorRecovery(t *testing.T, suite *TestSuite, mockPayPal *MockPayPalService) {
	// Test PayPal failure recovery
	testData := suite.GenerateTestMembership()
	submission := testData.ToMembershipSubmission()

	err := data.InsertMembership(submission)
	suite.AssertNoError(t, err)

	// Simulate PayPal failure
	mockPayPal.SetFailureMode(false, true, false) // Order creation fails

	_, err = mockPayPal.CreateOrder(testData.FormID, "100.00")
	suite.AssertError(t, err)
	t.Logf("✓ PayPal failure simulated and caught")

	// Reset and retry
	mockPayPal.SetFailureMode(false, false, false)

	order, err := mockPayPal.CreateOrder(testData.FormID, "100.00")
	suite.AssertNoError(t, err)
	t.Logf("✓ PayPal recovery successful (OrderID: %s)", order.ID)

	// Test database connection resilience
	// (This would require injecting connection failures, which is complex)

	t.Log("✅ Error recovery tests completed")
}

// Load tests (only run when -load flag is set)
func TestLoadTesting(t *testing.T) {
	if !*runLoad {
		t.Skip("Load testing disabled (use -load flag to enable)")
	}

	suite := NewTestSuite(t)
	mockPayPal := NewMockPayPalService()
	defer mockPayPal.Close()

	t.Run("ConcurrentFormSubmissions", func(t *testing.T) {
		testConcurrentFormSubmissions(t, suite, 50)
	})

	t.Run("HighVolumePayments", func(t *testing.T) {
		testHighVolumePayments(t, suite, mockPayPal, 100)
	})

	t.Run("DatabaseStress", func(t *testing.T) {
		testDatabaseStress(t, suite, 200)
	})
}

func testConcurrentFormSubmissions(t *testing.T, suite *TestSuite, numSubmissions int) {
	start := time.Now()

	results := make(chan error, numSubmissions)

	// Launch concurrent submissions
	for i := 0; i < numSubmissions; i++ {
		go func(id int) {
			testData := suite.GenerateTestMembership()
			testData.Email = fmt.Sprintf("load%d@test.com", id)
			submission := testData.ToMembershipSubmission()

			results <- data.InsertMembership(submission)
		}(i)
	}

	// Collect results
	var errors []error
	for i := 0; i < numSubmissions; i++ {
		if err := <-results; err != nil {
			errors = append(errors, err)
		}
	}

	duration := time.Since(start)
	successRate := float64(numSubmissions-len(errors)) / float64(numSubmissions) * 100

	t.Logf("Concurrent submissions: %d total, %d errors, %.1f%% success rate, %v duration",
		numSubmissions, len(errors), successRate, duration)

	if successRate < 95.0 {
		t.Errorf("Success rate too low: %.1f%%", successRate)
	}
}

func testHighVolumePayments(t *testing.T, suite *TestSuite, mockPayPal *MockPayPalService, numPayments int) {
	start := time.Now()

	// Pre-create memberships
	var testData []TestMembershipData
	for i := 0; i < numPayments; i++ {
		membershipData := suite.GenerateTestMembership() // Renamed to avoid confusion
		membershipData.Email = fmt.Sprintf("payment%d@test.com", i)
		submission := membershipData.ToMembershipSubmission()

		if err := data.InsertMembership(submission); err != nil {
			t.Fatalf("Failed to create test membership %d: %v", i, err)
		}

		testData = append(testData, membershipData)
	}

	setupDuration := time.Since(start)
	t.Logf("Setup completed in %v", setupDuration)

	// Run payment processing concurrently
	start = time.Now()
	results := make(chan error, numPayments)

	for i, td := range testData {
		go func(id int, membershipData TestMembershipData) {
			// Create and capture order
			order, err := mockPayPal.CreateOrder(membershipData.FormID, "100.00")
			if err != nil {
				results <- fmt.Errorf("order creation %d: %w", id, err)
				return
			}

			if err := mockPayPal.CaptureOrder(order.ID); err != nil {
				results <- fmt.Errorf("order capture %d: %w", id, err)
				return
			}

			// Update database with PayPal order
			now := time.Now()
			if err := data.UpdateMembershipPayPalOrder(membershipData.FormID, order.ID, &now); err != nil {
				results <- fmt.Errorf("db update %d: %w", id, err)
				return
			}

			// Update with capture details
			captureDetails := fmt.Sprintf(`{"id":"%s","status":"COMPLETED"}`, order.ID)
			if err := data.UpdateMembershipPayPalCapture(membershipData.FormID, captureDetails, "COMPLETED", &now); err != nil {
				results <- fmt.Errorf("capture update %d: %w", id, err)
				return
			}

			results <- nil
		}(i, td)
	}

	// Collect results
	var errors []error
	for i := 0; i < numPayments; i++ {
		if err := <-results; err != nil {
			errors = append(errors, err)
		}
	}

	duration := time.Since(start)
	throughput := float64(numPayments) / duration.Seconds()
	successRate := float64(numPayments-len(errors)) / float64(numPayments) * 100

	t.Logf("Payment processing: %d total, %d errors, %.1f%% success rate, %.1f payments/sec",
		numPayments, len(errors), successRate, throughput)

	if successRate < 95.0 {
		t.Errorf("Payment success rate too low: %.1f%%", successRate)
	}

	if throughput < 10.0 {
		t.Errorf("Throughput too low: %.1f payments/sec", throughput)
	}
}

func testDatabaseStress(t *testing.T, suite *TestSuite, numOperations int) {
	start := time.Now()

	results := make(chan error, numOperations)

	// Mix of different database operations
	for i := 0; i < numOperations; i++ {
		go func(id int) {
			switch id % 4 {
			case 0: // Insert membership
				testData := suite.GenerateTestMembership()
				testData.Email = fmt.Sprintf("stress%d@test.com", id)
				submission := testData.ToMembershipSubmission()
				results <- data.InsertMembership(submission)

			case 1: // Insert event
				testData := suite.GenerateTestEvent()
				testData.Email = fmt.Sprintf("stress%d@test.com", id)
				submission := testData.ToEventSubmission()
				results <- data.InsertEvent(submission)

			case 2: // Insert fundraiser
				testData := suite.GenerateTestFundraiser()
				testData.Email = fmt.Sprintf("stress%d@test.com", id)
				submission := testData.ToFundraiserSubmission()
				results <- data.InsertFundraiser(submission)

			case 3: // Query operations
				_, err := data.GetMembershipsByYear(time.Now().Year())
				results <- err
			}
		}(i)
	}

	// Collect results
	var errors []error
	for i := 0; i < numOperations; i++ {
		if err := <-results; err != nil {
			errors = append(errors, err)
		}
	}

	duration := time.Since(start)
	opsPerSecond := float64(numOperations) / duration.Seconds()
	successRate := float64(numOperations-len(errors)) / float64(numOperations) * 100

	t.Logf("Database stress test: %d operations, %d errors, %.1f%% success rate, %.1f ops/sec",
		numOperations, len(errors), successRate, opsPerSecond)

	if successRate < 99.0 {
		t.Errorf("Database success rate too low: %.1f%%", successRate)
	}
}

// Benchmark tests
func BenchmarkMembershipInsert(b *testing.B) {
	suite := NewTestSuite(&testing.T{})
	defer suite.Cleanup()

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		testData := suite.GenerateTestMembership()
		testData.Email = fmt.Sprintf("bench%d@test.com", i)
		submission := testData.ToMembershipSubmission()

		if err := data.InsertMembership(submission); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInventoryCalculation(b *testing.B) {
	suite := NewTestSuite(&testing.T{})
	defer suite.Cleanup()

	membership := "Premium Membership"
	addons := []string{"T-Shirt", "Sticker Pack"}
	fees := map[string]int{"Spring Festival Fee": 2}
	donation := 25.0

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := suite.Inventory.CalculateMembershipTotal(membership, addons, fees, donation, true)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTokenGeneration(b *testing.B) {
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := security.GenerateAccessToken()
		if err != nil {
			b.Fatal(err)
		}
	}
}
