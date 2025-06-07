package cleanup

import (
	"time"

	"sbcbackend/internal/data"
	"sbcbackend/internal/logger"
)

const (
	cleanupHour       = 2  // 2 AM
	retentionHours    = 48 // 48 hours
	maxDeletionPerRun = 25 // Maximum records to delete per run
)

// StartCleanupRoutine starts the daily cleanup job
func StartCleanupRoutine() {
	go func() {
		logger.LogInfo("Cleanup routine started - will run daily at %d:00 AM", cleanupHour)

		for {
			// Calculate next 2 AM
			now := time.Now()
			next2AM := time.Date(now.Year(), now.Month(), now.Day(), cleanupHour, 0, 0, 0, now.Location())

			// If it's past 2 AM today, schedule for tomorrow
			if now.After(next2AM) {
				next2AM = next2AM.Add(24 * time.Hour)
			}

			sleepDuration := next2AM.Sub(now)
			logger.LogInfo("Next cleanup scheduled for %v (in %v)", next2AM.Format("2006-01-02 15:04:05"), sleepDuration)

			time.Sleep(sleepDuration)

			// Run cleanup
			runCleanup()
		}
	}()
}

// runCleanup performs the actual cleanup of abandoned records
func runCleanup() {
	logger.LogInfo("Starting daily cleanup of abandoned form submissions")

	cutoffTime := time.Now().Add(-retentionHours * time.Hour)
	logger.LogInfo("Cleaning records older than %v (before %v)",
		time.Duration(retentionHours)*time.Hour, cutoffTime.Format("2006-01-02 15:04:05"))

	totalCleaned := 0

	// Clean membership submissions
	membershipCleaned, err := cleanupMembershipSubmissions(cutoffTime)
	if err != nil {
		logger.LogError("Failed to cleanup membership submissions: %v", err)
	} else {
		totalCleaned += membershipCleaned
		if membershipCleaned > 0 {
			logger.LogInfo("Cleaned up %d abandoned membership submissions", membershipCleaned)
		}
	}

	// Clean event submissions
	eventCleaned, err := cleanupEventSubmissions(cutoffTime)
	if err != nil {
		logger.LogError("Failed to cleanup event submissions: %v", err)
	} else {
		totalCleaned += eventCleaned
		if eventCleaned > 0 {
			logger.LogInfo("Cleaned up %d abandoned event submissions", eventCleaned)
		}
	}

	// Clean fundraiser submissions
	fundraiserCleaned, err := cleanupFundraiserSubmissions(cutoffTime)
	if err != nil {
		logger.LogError("Failed to cleanup fundraiser submissions: %v", err)
	} else {
		totalCleaned += fundraiserCleaned
		if fundraiserCleaned > 0 {
			logger.LogInfo("Cleaned up %d abandoned fundraiser submissions", fundraiserCleaned)
		}
	}

	if totalCleaned == 0 {
		logger.LogInfo("Cleanup completed - no abandoned records found")
	} else {
		logger.LogInfo("Cleanup completed - total %d abandoned records removed", totalCleaned)
	}
}

func cleanupMembershipSubmissions(cutoffTime time.Time) (int, error) {
	const stmt = `
		DELETE FROM membership_submissions 
		WHERE form_id IN (
			SELECT form_id FROM membership_submissions 
			WHERE submitted = 0 
			AND submission_date < ? 
			LIMIT ?
		)`

	result, err := data.ExecDB(stmt, cutoffTime.Format(time.RFC3339), maxDeletionPerRun)
	if err != nil {
		return 0, err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	return int(rowsAffected), nil
}

func cleanupEventSubmissions(cutoffTime time.Time) (int, error) {
	const stmt = `
		DELETE FROM event_submissions 
		WHERE form_id IN (
			SELECT form_id FROM event_submissions 
			WHERE submitted = 0 
			AND submission_date < ? 
			LIMIT ?
		)`

	result, err := data.ExecDB(stmt, cutoffTime.Format(time.RFC3339), maxDeletionPerRun)
	if err != nil {
		return 0, err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	return int(rowsAffected), nil
}

func cleanupFundraiserSubmissions(cutoffTime time.Time) (int, error) {
	const stmt = `
		DELETE FROM fundraiser_submissions 
		WHERE form_id IN (
			SELECT form_id FROM fundraiser_submissions 
			WHERE submitted = 0 
			AND submission_date < ? 
			LIMIT ?
		)`

	result, err := data.ExecDB(stmt, cutoffTime.Format(time.RFC3339), maxDeletionPerRun)
	if err != nil {
		return 0, err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	return int(rowsAffected), nil
}
