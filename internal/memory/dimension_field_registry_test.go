package memory

import (
	"testing"
)

func TestNewDimensionFieldRegistry(t *testing.T) {
	registry := NewDimensionFieldRegistry()

	dims := registry.GetAllDimensions()
	if len(dims) != 3 {
		t.Errorf("Expected 3 dimensions, got %d", len(dims))
	}

	expectedDims := map[string]bool{"user": false, "feedback": false, "reference": false}
	for _, d := range dims {
		if _, ok := expectedDims[d]; ok {
			expectedDims[d] = true
		}
	}
	for d, found := range expectedDims {
		if !found {
			t.Errorf("Expected dimension %q not found", d)
		}
	}
}

func TestGetValidFields(t *testing.T) {
	registry := NewDimensionFieldRegistry()

	userFields := registry.GetValidFields("user")
	if len(userFields) == 0 {
		t.Error("Expected user fields, got none")
	}

	feedbackFields := registry.GetValidFields("feedback")
	if len(feedbackFields) != 2 {
		t.Errorf("Expected 2 feedback fields, got %d", len(feedbackFields))
	}

	invalidFields := registry.GetValidFields("nonexistent")
	if invalidFields != nil {
		t.Error("Expected nil for invalid dimension")
	}
}

func TestValidate_Dimension(t *testing.T) {
	registry := NewDimensionFieldRegistry()

	// Valid dimension
	result := registry.Validate("user", "name", "set", "John Doe", "")
	if !result.Valid {
		t.Errorf("Expected valid result, got errors: %v", result.Errors)
	}
	if result.Resolved.Dimension != "user" {
		t.Errorf("Expected dimension 'user', got %q", result.Resolved.Dimension)
	}

	// Invalid dimension
	result = registry.Validate("invalid", "name", "set", "John Doe", "")
	if result.Valid {
		t.Error("Expected invalid result for unknown dimension")
	}
	if len(result.Errors) == 0 {
		t.Error("Expected at least one error")
	}
	if result.Errors[0].Code != "INVALID_DIMENSION" {
		t.Errorf("Expected INVALID_DIMENSION error, got %q", result.Errors[0].Code)
	}
}

func TestValidate_Field(t *testing.T) {
	registry := NewDimensionFieldRegistry()

	// Valid field
	result := registry.Validate("user", "name", "set", "John Doe", "")
	if !result.Valid {
		t.Errorf("Expected valid result, got errors: %v", result.Errors)
	}

	// Invalid field
	result = registry.Validate("user", "nonexistent", "set", "value", "")
	if result.Valid {
		t.Error("Expected invalid result for unknown field")
	}
	if result.Errors[0].Code != "INVALID_FIELD" {
		t.Errorf("Expected INVALID_FIELD error, got %q", result.Errors[0].Code)
	}
}

func TestValidate_LegacyFieldAutoCorrect(t *testing.T) {
	registry := NewDimensionFieldRegistry()

	// Test legacy field names auto-correct to canonical names
	testCases := []struct {
		dimension string
		legacy    string
		canonical string
	}{
		{"feedback", "correction", "corrections"},
		{"feedback", "endorsement", "endorsements"},
		{"reference", "resource", "resources"},
	}

	for _, tc := range testCases {
		result := registry.Validate(tc.dimension, tc.legacy, "add", map[string]string{"test": "value"}, "")
		if !result.Valid {
			t.Errorf("Expected valid result for legacy field '%s' in '%s', got errors: %v", tc.legacy, tc.dimension, result.Errors)
			continue
		}
		if !result.Corrected {
			t.Errorf("Expected auto-correction for legacy field '%s' in '%s'", tc.legacy, tc.dimension)
		}
		if result.Resolved.Field != tc.canonical {
			t.Errorf("Expected corrected field '%s', got '%s'", tc.canonical, result.Resolved.Field)
		}
	}
}

func TestValidate_Action(t *testing.T) {
	registry := NewDimensionFieldRegistry()

	// Valid action for scalar field
	result := registry.Validate("user", "name", "set", "John Doe", "")
	if !result.Valid {
		t.Errorf("Expected valid result, got errors: %v", result.Errors)
	}

	// Invalid action for scalar field (add not allowed on scalars)
	result = registry.Validate("user", "name", "add", "John Doe", "")
	if result.Valid {
		t.Error("Expected invalid result for invalid action")
	}
	if result.Errors[0].Code != "INVALID_ACTION" {
		t.Errorf("Expected INVALID_ACTION error, got %q", result.Errors[0].Code)
	}

	// Valid add action for array field
	result = registry.Validate("user", "expertise", "add", "Go", "")
	if !result.Valid {
		t.Errorf("Expected valid result for add action on array field, got errors: %v", result.Errors)
	}
}

func TestValidate_RemoveRequiresItemID(t *testing.T) {
	registry := NewDimensionFieldRegistry()

	// Remove on array field without item_id should fail
	result := registry.Validate("user", "expertise", "remove", nil, "")
	if result.Valid {
		t.Error("Expected invalid result for remove without item_id")
	}
	if result.Errors[0].Code != "MISSING_ITEM_ID" {
		t.Errorf("Expected MISSING_ITEM_ID error, got %q", result.Errors[0].Code)
	}

	// Remove on array field with item_id should pass
	result = registry.Validate("user", "expertise", "remove", nil, "expert-1")
	if !result.Valid {
		t.Errorf("Expected valid result for remove with item_id, got errors: %v", result.Errors)
	}
	if result.Resolved.ItemID != "expert-1" {
		t.Errorf("Expected item_id 'expert-1', got '%s'", result.Resolved.ItemID)
	}
}

func TestFormatSchemaAsJSON(t *testing.T) {
	registry := NewDimensionFieldRegistry()

	// Valid dimension
	schemaJSON := registry.FormatSchemaAsJSON("user")
	if schemaJSON == "" {
		t.Error("Expected non-empty schema JSON for user dimension")
	}

	// Invalid dimension
	schemaJSON = registry.FormatSchemaAsJSON("nonexistent")
	if schemaJSON != "" {
		t.Error("Expected empty schema JSON for invalid dimension")
	}
}

func TestGetDimensionSchema(t *testing.T) {
	registry := NewDimensionFieldRegistry()

	// Valid dimension
	schema := registry.GetDimensionSchema("user")
	if schema == nil {
		t.Error("Expected non-nil schema for user dimension")
	}
	if schema.Dimension != "user" {
		t.Errorf("Expected dimension name 'user', got %q", schema.Dimension)
	}
	if len(schema.Fields) == 0 {
		t.Error("Expected user schema to have fields")
	}

	// Invalid dimension
	schema = registry.GetDimensionSchema("nonexistent")
	if schema != nil {
		t.Error("Expected nil schema for invalid dimension")
	}
}

func TestResolvedUpdate_Structure(t *testing.T) {
	registry := NewDimensionFieldRegistry()

	// Test a complete valid update
	result := registry.Validate("feedback", "corrections", "add",
		map[string]interface{}{
			"topic":  "code-style",
			"wrong":  "tabs",
			"correct": "spaces",
		},
		"",
	)

	if !result.Valid {
		t.Fatalf("Expected valid result, got errors: %v", result.Errors)
	}

	if result.Resolved.Dimension != "feedback" {
		t.Errorf("Expected dimension 'feedback', got %q", result.Resolved.Dimension)
	}
	if result.Resolved.Field != "corrections" {
		t.Errorf("Expected field 'corrections', got %q", result.Resolved.Field)
	}
	if result.Resolved.Action != "add" {
		t.Errorf("Expected action 'add', got %q", result.Resolved.Action)
	}
}

func TestValidate_EmptyReasonHandling(t *testing.T) {
	registry := NewDimensionFieldRegistry()

	// Empty dimension
	result := registry.Validate("", "name", "set", "John", "")
	if result.Valid {
		t.Error("Expected invalid result for empty dimension")
	}

	// Empty field
	result = registry.Validate("user", "", "set", "John", "")
	if result.Valid {
		t.Error("Expected invalid result for empty field")
	}

	// Empty action
	result = registry.Validate("user", "name", "", "John", "")
	if result.Valid {
		t.Error("Expected invalid result for empty action")
	}
}

func TestFieldTypeConstants(t *testing.T) {
	if FieldTypeScalar != "scalar" {
		t.Errorf("Expected FieldTypeScalar to be 'scalar', got %q", FieldTypeScalar)
	}
	if FieldTypeArray != "array" {
		t.Errorf("Expected FieldTypeArray to be 'array', got %q", FieldTypeArray)
	}
	if FieldTypeMap != "map" {
		t.Errorf("Expected FieldTypeMap to be 'map', got %q", FieldTypeMap)
	}
}

func TestFieldSchema_LegacyNames(t *testing.T) {
	registry := NewDimensionFieldRegistry()
	schema := registry.GetDimensionSchema("feedback")

	correctionsField := schema.Fields["corrections"]
	if len(correctionsField.LegacyNames) == 0 {
		t.Error("Expected corrections field to have legacy names")
	}

	endorsementsField := schema.Fields["endorsements"]
	if len(endorsementsField.LegacyNames) == 0 {
		t.Error("Expected endorsements field to have legacy names")
	}
}
