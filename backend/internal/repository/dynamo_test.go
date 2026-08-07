package repository

import (
	"testing"
)

func TestKeyMappingHelpers(t *testing.T) {
	tests := []struct {
		name     string
		function func() string
		expected string
	}{
		{
			name: "userPK formats user ID correctly",
			function: func() string {
				return userPK("user123")
			},
			expected: "USER#user123",
		},
		{
			name: "userPK handles empty user ID",
			function: func() string {
				return userPK("")
			},
			expected: "USER#",
		},
		{
			name: "userPK handles special characters",
			function: func() string {
				return userPK("user@example.com")
			},
			expected: "USER#user@example.com",
		},
		{
			name: "settingsSK returns constant value",
			function: func() string {
				return settingsSK()
			},
			expected: "SETTINGS",
		},
		{
			name: "noteSK formats note ID correctly",
			function: func() string {
				return noteSK("note456")
			},
			expected: "NOTE#note456",
		},
		{
			name: "noteSK handles empty note ID",
			function: func() string {
				return noteSK("")
			},
			expected: "NOTE#",
		},
		{
			name: "noteSK handles UUID-style ID",
			function: func() string {
				return noteSK("123e4567-e89b-12d3-a456-426614174000")
			},
			expected: "NOTE#123e4567-e89b-12d3-a456-426614174000",
		},
		{
			name: "captureSK formats capture ID correctly",
			function: func() string {
				return captureSK("capture789")
			},
			expected: "CAPTURE#capture789",
		},
		{
			name: "captureSK handles empty capture ID",
			function: func() string {
				return captureSK("")
			},
			expected: "CAPTURE#",
		},
		{
			name: "captureSK handles UUID-style ID",
			function: func() string {
				return captureSK("987fcdeb-51d2-43a1-b678-123456789abc")
			},
			expected: "CAPTURE#987fcdeb-51d2-43a1-b678-123456789abc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.function()
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestKeyMappingConsistency(t *testing.T) {
	userID := "testuser"
	noteID := "testnote"
	captureID := "testcapture"

	// Test that keys are consistent across calls
	pk1 := userPK(userID)
	pk2 := userPK(userID)
	if pk1 != pk2 {
		t.Errorf("userPK is not consistent: %q != %q", pk1, pk2)
	}

	sk1 := settingsSK()
	sk2 := settingsSK()
	if sk1 != sk2 {
		t.Errorf("settingsSK is not consistent: %q != %q", sk1, sk2)
	}

	noteSK1 := noteSK(noteID)
	noteSK2 := noteSK(noteID)
	if noteSK1 != noteSK2 {
		t.Errorf("noteSK is not consistent: %q != %q", noteSK1, noteSK2)
	}

	captureSK1 := captureSK(captureID)
	captureSK2 := captureSK(captureID)
	if captureSK1 != captureSK2 {
		t.Errorf("captureSK is not consistent: %q != %q", captureSK1, captureSK2)
	}
}

func TestKeyMappingUniqueness(t *testing.T) {
	noteID := "testnote"
	captureID := "testcapture"

	settingSK := settingsSK()
	noteSKResult := noteSK(noteID)
	captureSKResult := captureSK(captureID)

	// Ensure different SK types don't conflict
	keys := []string{settingSK, noteSKResult, captureSKResult}
	for i, key1 := range keys {
		for j, key2 := range keys {
			if i != j && key1 == key2 {
				t.Errorf("SK collision detected: %q == %q", key1, key2)
			}
		}
	}

	// Ensure SK prefixes are distinct
	if settingSK == noteSKResult || settingSK == captureSKResult || noteSKResult == captureSKResult {
		t.Error("SK values should be distinct for different entity types")
	}
}

func TestKeyMappingPrefixes(t *testing.T) {
	// Test that different entity types have distinguishable prefixes
	testNote := noteSK("anything")
	testCapture := captureSK("anything")
	testSettings := settingsSK()

	if testNote[:4] != "NOTE" {
		t.Errorf("noteSK should start with 'NOTE', got %q", testNote[:4])
	}

	if testCapture[:7] != "CAPTURE" {
		t.Errorf("captureSK should start with 'CAPTURE', got %q", testCapture[:7])
	}

	if testSettings != "SETTINGS" {
		t.Errorf("settingsSK should be exactly 'SETTINGS', got %q", testSettings)
	}
}