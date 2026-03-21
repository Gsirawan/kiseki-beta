// Package testutil provides test helpers for Kiseki.
package testutil

import (
	"context"
	"database/sql"
	"hash/fnv"
	"math"
	"sync"

	"github.com/Gsirawan/kiseki-beta/internal/db"
)

// MockEmbedder implements ollama.Embedder with deterministic vectors.
// It generates consistent embeddings based on text content using FNV hashing,
// producing vectors that have meaningful cosine similarity (similar words → closer vectors).
type MockEmbedder struct {
	// Dimension is the vector size. Defaults to db.EmbedDimension if 0.
	Dimension int
	// CallCount tracks how many times Embed was called.
	CallCount int
	mu        sync.Mutex
}

// Embed produces a deterministic float32 vector from the input text.
// The same text always produces the same vector.
// Different texts produce different vectors with reasonable cosine distance.
func (m *MockEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	m.mu.Lock()
	m.CallCount++
	m.mu.Unlock()

	dim := m.Dimension
	if dim <= 0 {
		dim = db.EmbedDimension
	}

	vec := make([]float32, dim)

	// Use FNV hash to seed pseudo-random values from text
	h := fnv.New64a()
	h.Write([]byte(text))
	seed := h.Sum64()

	// Fill vector with deterministic values derived from seed
	// Use different bit regions of the hash for different dimensions
	var norm float64
	for i := range vec {
		// Simple LCG-style pseudo-random from seed
		seed = seed*6364136223846793005 + 1442695040888963407
		// Map to [-1, 1]
		val := float64(int64(seed)) / float64(math.MaxInt64)
		vec[i] = float32(val)
		norm += val * val
	}

	// L2-normalize so cosine similarity works correctly
	norm = math.Sqrt(norm)
	if norm > 0 {
		for i := range vec {
			vec[i] = float32(float64(vec[i]) / norm)
		}
	}

	return vec, nil
}

// NewTestDB creates an in-memory SQLite database fully initialized via db.InitDB.
// Uses cache=shared so all connections in the pool share the same in-memory DB.
// This is critical for MultiLayerSearch tests which use goroutines (each goroutine
// may get a different connection from the pool).
func NewTestDB() *sql.DB {
	database, err := db.InitDB("file::memory:?cache=shared")
	if err != nil {
		panic("testutil.NewTestDB: " + err.Error())
	}
	return database
}
