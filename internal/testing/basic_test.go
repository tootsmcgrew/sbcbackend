package testing

import (
	"testing"
)

func TestBasic(t *testing.T) {
	t.Log("Basic test running")

	// Test that we can create a test suite
	suite := NewTestSuite(t)

	// Test that we can generate test data
	testData := suite.GenerateTestMembership()

	if testData.FormID == "" {
		t.Error("FormID should not be empty")
	}

	if testData.Email == "" {
		t.Error("Email should not be empty")
	}

	t.Logf("✅ Basic test passed - FormID: %s", testData.FormID)
}
