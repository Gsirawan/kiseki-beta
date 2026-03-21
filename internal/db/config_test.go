package db

import (
	"strings"
	"testing"
)

func TestValidateEmbedDimConfig_FirstRun(t *testing.T) {
	database, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	defer database.Close()

	// After InitDB, embed_dim should be stored
	stored := StoredEmbedDim(database)
	if stored != EmbedDimension {
		t.Errorf("expected stored embed dim %d, got %d", EmbedDimension, stored)
	}
}

func TestValidateEmbedDimConfig_MatchingDim(t *testing.T) {
	database, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	defer database.Close()

	// Running validation again with same dimension should succeed
	err = ValidateEmbedDimConfig(database)
	if err != nil {
		t.Errorf("expected no error for matching dimension, got: %v", err)
	}
}

func TestValidateEmbedDimConfig_MismatchedDim(t *testing.T) {
	database, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	defer database.Close()

	// Store a different dimension to simulate mismatch
	if err := SetConfig(database, "embed_dim", "768"); err != nil {
		t.Fatalf("set config: %v", err)
	}

	// Now validation should fail
	err = ValidateEmbedDimConfig(database)
	if err == nil {
		t.Fatal("expected error for mismatched dimension, got nil")
	}

	// Error message should mention both dimensions
	errMsg := err.Error()
	if !strings.Contains(errMsg, "768") {
		t.Errorf("error should mention stored dim 768: %s", errMsg)
	}
	if !strings.Contains(errMsg, "1024") {
		t.Errorf("error should mention configured dim 1024: %s", errMsg)
	}
	if !strings.Contains(errMsg, "forget --all") {
		t.Errorf("error should suggest 'forget --all' recovery path: %s", errMsg)
	}
}

func TestValidateEmbedDimConfig_DifferentEnvDim(t *testing.T) {
	// Save original and restore after test
	origDim := EmbedDimension
	defer func() { EmbedDimension = origDim }()

	// Create DB with dim=1024
	EmbedDimension = 1024
	database, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	defer database.Close()

	// Change env dimension
	EmbedDimension = 768

	// Validation should fail
	err = ValidateEmbedDimConfig(database)
	if err == nil {
		t.Fatal("expected error when EMBED_DIM changed from 1024 to 768")
	}
	if !strings.Contains(err.Error(), "1024") || !strings.Contains(err.Error(), "768") {
		t.Errorf("error should mention both dimensions: %s", err.Error())
	}
}

func TestSetGetConfig(t *testing.T) {
	database, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	defer database.Close()

	// Set and get
	if err := SetConfig(database, "test_key", "test_value"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	val, err := GetConfig(database, "test_key")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if val != "test_value" {
		t.Errorf("expected 'test_value', got %q", val)
	}

	// Get non-existent key
	val, err = GetConfig(database, "nonexistent")
	if err != nil {
		t.Fatalf("GetConfig for nonexistent key: %v", err)
	}
	if val != "" {
		t.Errorf("expected empty string for nonexistent key, got %q", val)
	}

	// Overwrite
	if err := SetConfig(database, "test_key", "new_value"); err != nil {
		t.Fatalf("SetConfig overwrite: %v", err)
	}
	val, err = GetConfig(database, "test_key")
	if err != nil {
		t.Fatalf("GetConfig after overwrite: %v", err)
	}
	if val != "new_value" {
		t.Errorf("expected 'new_value', got %q", val)
	}
}

func TestStoredEmbedDim_NoConfig(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	// No config table at all → should return 0, not panic
	dim := StoredEmbedDim(db)
	if dim != 0 {
		t.Errorf("expected 0 for DB without config table, got %d", dim)
	}
}
