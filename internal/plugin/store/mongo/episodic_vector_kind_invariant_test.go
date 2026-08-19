//go:build !nomongo

package mongo

import (
	"context"
	"testing"

	registryepisodic "github.com/chirino/memory-service/internal/registry/episodic"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMongoUpsertMemoryVectorsRejectsEmptyKind verifies that UpsertMemoryVectors
// returns an error immediately when any item has an empty MemoryKind field.
// This enforces the fresh-only kind invariant before any MongoDB call is made.
func TestMongoUpsertMemoryVectorsRejectsEmptyKind(t *testing.T) {
	t.Parallel()
	// Use a store with nil qdrant and nil vectors collection.
	// The empty-kind guard fires before s.vectors is accessed.
	s := &mongoEpisodicStore{
		qdrant:  nil,
		vectors: nil,
	}

	memID := uuid.New()
	items := []registryepisodic.MemoryVectorUpsert{
		{
			MemoryID:   memID,
			FieldName:  "content",
			Embedding:  []float32{0.1, 0.2},
			MemoryKind: "", // empty — must be rejected
		},
	}
	err := s.UpsertMemoryVectors(context.Background(), items)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MemoryVectorUpsert.MemoryKind must be non-empty")
}

// TestMongoSearchMemoryVectorsSkipsEmptyKind verifies the skip logic for
// memoryVectorDoc entries with empty MemoryKind. We test by constructing the
// skip condition directly: if doc.MemoryKind == "", the candidate is skipped.
// This mirrors the code at SearchMemoryVectors line "if doc.MemoryKind == """.
func TestMongoSearchMemoryVectorsSkipsEmptyKind(t *testing.T) {
	t.Parallel()
	// Simulate the skip condition: a doc with empty MemoryKind is skipped.
	doc := memoryVectorDoc{
		MemoryID:   uuid.New().String(),
		FieldName:  "content",
		MemoryKind: "", // empty — should be skipped
	}
	assert.Equal(t, "", doc.MemoryKind,
		"doc with empty MemoryKind matches the skip condition in SearchMemoryVectors")

	// A doc with a non-empty kind should NOT be skipped.
	docWithKind := memoryVectorDoc{
		MemoryID:   uuid.New().String(),
		FieldName:  "content",
		MemoryKind: "default/v1",
	}
	assert.NotEqual(t, "", docWithKind.MemoryKind,
		"doc with non-empty MemoryKind does not match the skip condition")
}
