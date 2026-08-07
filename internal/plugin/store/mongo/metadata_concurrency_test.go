//go:build !nomongo

package mongo

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/chirino/memory-service/internal/config"
	registrymigrate "github.com/chirino/memory-service/internal/registry/migrate"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
	"github.com/chirino/memory-service/internal/testutil/testbarrier"
	"github.com/chirino/memory-service/internal/testutil/testmongo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// normalizeMetadata converts metadata through JSON marshal/unmarshal to avoid BSON type assertions.
func normalizeMetadata(t *testing.T, metadata map[string]interface{}) map[string]interface{} {
	t.Helper()
	b, err := json.Marshal(metadata)
	require.NoError(t, err)
	var result map[string]interface{}
	err = json.Unmarshal(b, &result)
	require.NoError(t, err)
	return result
}

func TestMongoMetadataConcurrentPatch(t *testing.T) {
	dbURL := testmongo.StartMongo(t)
	cfg := config.DefaultConfig()
	cfg.DBURL = dbURL
	ctx := config.WithContext(context.Background(), &cfg)

	err := registrymigrate.RunAll(ctx)
	require.NoError(t, err)

	loader, err := registrystore.Select("mongo")
	require.NoError(t, err)
	store, err := loader(ctx)
	require.NoError(t, err)
	mongoStore, ok := store.(*MongoStore)
	require.True(t, ok)

	// Create conversation with base metadata
	conv, err := store.CreateConversation(ctx, "user1", "client1", "Test", map[string]interface{}{
		"base": "x",
	}, nil, nil, nil)
	require.NoError(t, err)

	// Concurrently patch different keys
	var wg sync.WaitGroup
	barrier := testbarrier.New(2)
	mongoStore.metadataPatchBeforeUpdate = barrier.Wait
	t.Cleanup(func() { mongoStore.metadataPatchBeforeUpdate = nil })
	errs := make(chan error, 2)
	wg.Add(2)

	go func() {
		defer wg.Done()
		_, err := store.UpdateConversation(ctx, "user1", conv.ID, nil, map[string]interface{}{
			"left": "1",
		})
		errs <- err
	}()

	go func() {
		defer wg.Done()
		_, err := store.UpdateConversation(ctx, "user1", conv.ID, nil, map[string]interface{}{
			"right": "2",
		})
		errs <- err
	}()

	barrier.WaitForParties(t, 2)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	// Verify all three keys remain (normalize through JSON to avoid BSON type assertions)
	result, err := store.GetConversation(ctx, "user1", conv.ID)
	require.NoError(t, err)
	normalized := normalizeMetadata(t, result.Metadata)
	assert.Equal(t, "x", normalized["base"])
	assert.Equal(t, "1", normalized["left"])
	assert.Equal(t, "2", normalized["right"])
}

func TestMongoMetadataExplicitNullDeletion(t *testing.T) {
	dbURL := testmongo.StartMongo(t)
	cfg := config.DefaultConfig()
	cfg.DBURL = dbURL
	ctx := config.WithContext(context.Background(), &cfg)

	err := registrymigrate.RunAll(ctx)
	require.NoError(t, err)

	loader, err := registrystore.Select("mongo")
	require.NoError(t, err)
	store, err := loader(ctx)
	require.NoError(t, err)

	// Create conversation with nested metadata
	conv, err := store.CreateConversation(ctx, "user1", "client1", "Test", map[string]interface{}{
		"keep":   "value",
		"delete": "old",
		"nested": map[string]interface{}{
			"inner": nil, // stored null
		},
	}, nil, nil, nil)
	require.NoError(t, err)

	// Patch with explicit null to delete "delete" key
	_, err = store.UpdateConversation(ctx, "user1", conv.ID, nil, map[string]interface{}{
		"delete": nil,
	})
	require.NoError(t, err)

	// Verify "delete" key removed, others preserved (normalize through JSON)
	result, err := store.GetConversation(ctx, "user1", conv.ID)
	require.NoError(t, err)
	normalized := normalizeMetadata(t, result.Metadata)
	assert.Equal(t, "value", normalized["keep"])
	assert.NotContains(t, normalized, "delete")
	// Nested structure with stored null should remain
	assert.Contains(t, normalized, "nested")
	nested := normalized["nested"].(map[string]interface{})
	assert.Contains(t, nested, "inner")
	assert.Nil(t, nested["inner"])
}

func TestMongoMetadataNestedObjectReplacement(t *testing.T) {
	dbURL := testmongo.StartMongo(t)
	cfg := config.DefaultConfig()
	cfg.DBURL = dbURL
	ctx := config.WithContext(context.Background(), &cfg)

	err := registrymigrate.RunAll(ctx)
	require.NoError(t, err)

	loader, err := registrystore.Select("mongo")
	require.NoError(t, err)
	store, err := loader(ctx)
	require.NoError(t, err)

	// Create conversation with nested metadata
	conv, err := store.CreateConversation(ctx, "user1", "client1", "Test", map[string]interface{}{
		"obj": map[string]interface{}{
			"a": "1",
			"b": "2",
		},
	}, nil, nil, nil)
	require.NoError(t, err)

	// Replace entire top-level "obj" with new nested object
	_, err = store.UpdateConversation(ctx, "user1", conv.ID, nil, map[string]interface{}{
		"obj": map[string]interface{}{
			"c": "3",
		},
	})
	require.NoError(t, err)

	// Verify old nested keys are gone, new nested key exists (normalize through JSON)
	result, err := store.GetConversation(ctx, "user1", conv.ID)
	require.NoError(t, err)
	normalized := normalizeMetadata(t, result.Metadata)
	assert.Contains(t, normalized, "obj")
	obj := normalized["obj"].(map[string]interface{})
	assert.NotContains(t, obj, "a")
	assert.NotContains(t, obj, "b")
	assert.Equal(t, "3", obj["c"])
}
