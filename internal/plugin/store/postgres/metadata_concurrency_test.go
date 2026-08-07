//go:build !nopostgresql

package postgres

import (
	"context"
	"sync"
	"testing"

	"github.com/chirino/memory-service/internal/config"
	registrymigrate "github.com/chirino/memory-service/internal/registry/migrate"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
	"github.com/chirino/memory-service/internal/testutil/testbarrier"
	"github.com/chirino/memory-service/internal/testutil/testpg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostgresMetadataConcurrentPatch(t *testing.T) {
	dbURL := testpg.StartPostgres(t)
	cfg := config.DefaultConfig()
	cfg.DBURL = dbURL
	ctx := config.WithContext(context.Background(), &cfg)

	err := registrymigrate.RunAll(ctx)
	require.NoError(t, err)

	loader, err := registrystore.Select("postgres")
	require.NoError(t, err)
	store, err := loader(ctx)
	require.NoError(t, err)
	pgStore, ok := store.(*PostgresStore)
	require.True(t, ok)

	// Create conversation with base metadata
	conv, err := store.CreateConversation(ctx, "user1", "client1", "Test", map[string]interface{}{
		"base": "x",
	}, nil, nil, nil)
	require.NoError(t, err)

	// Concurrently patch different keys
	var wg sync.WaitGroup
	barrier := testbarrier.New(2)
	pgStore.metadataPatchBeforeUpdate = barrier.Wait
	t.Cleanup(func() { pgStore.metadataPatchBeforeUpdate = nil })
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

	// Verify all three keys remain
	result, err := store.GetConversation(ctx, "user1", conv.ID)
	require.NoError(t, err)
	assert.Equal(t, "x", result.Metadata["base"])
	assert.Equal(t, "1", result.Metadata["left"])
	assert.Equal(t, "2", result.Metadata["right"])
}

func TestPostgresMetadataExplicitNullDeletion(t *testing.T) {
	dbURL := testpg.StartPostgres(t)
	cfg := config.DefaultConfig()
	cfg.DBURL = dbURL
	ctx := config.WithContext(context.Background(), &cfg)

	err := registrymigrate.RunAll(ctx)
	require.NoError(t, err)

	loader, err := registrystore.Select("postgres")
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

	// Verify "delete" key removed, others preserved
	result, err := store.GetConversation(ctx, "user1", conv.ID)
	require.NoError(t, err)
	assert.Equal(t, "value", result.Metadata["keep"])
	assert.NotContains(t, result.Metadata, "delete")
	// Nested structure with stored null should remain
	nested, ok := result.Metadata["nested"].(map[string]interface{})
	require.True(t, ok)
	assert.Contains(t, nested, "inner")
	assert.Nil(t, nested["inner"])
}

func TestPostgresMetadataNestedObjectReplacement(t *testing.T) {
	dbURL := testpg.StartPostgres(t)
	cfg := config.DefaultConfig()
	cfg.DBURL = dbURL
	ctx := config.WithContext(context.Background(), &cfg)

	err := registrymigrate.RunAll(ctx)
	require.NoError(t, err)

	loader, err := registrystore.Select("postgres")
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

	// Verify old nested keys are gone, new nested key exists
	result, err := store.GetConversation(ctx, "user1", conv.ID)
	require.NoError(t, err)
	obj, ok := result.Metadata["obj"].(map[string]interface{})
	require.True(t, ok)
	assert.NotContains(t, obj, "a")
	assert.NotContains(t, obj, "b")
	assert.Equal(t, "3", obj["c"])
}

func TestPostgresMetadataArbitraryKeysAndEmptyPatch(t *testing.T) {
	dbURL := testpg.StartPostgres(t)
	cfg := config.DefaultConfig()
	cfg.DBURL = dbURL
	ctx := config.WithContext(context.Background(), &cfg)

	err := registrymigrate.RunAll(ctx)
	require.NoError(t, err)

	loader, err := registrystore.Select("postgres")
	require.NoError(t, err)
	store, err := loader(ctx)
	require.NoError(t, err)

	conv, err := store.CreateConversation(ctx, "user1", "client1", "Test", map[string]interface{}{
		"unrelated": "keep",
	}, nil, nil, nil)
	require.NoError(t, err)

	updated, err := store.UpdateConversation(ctx, "user1", conv.ID, nil, registrystore.MetadataPatch{
		"a.b":            "dotted",
		"key with space": "spaced",
		"key\"quote":     "quoted",
		"key\\slash":     "slashed",
		"$status": map[string]interface{}{
			"state": "$pending",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "keep", updated.Metadata["unrelated"])
	assert.Equal(t, "dotted", updated.Metadata["a.b"])
	assert.Equal(t, "spaced", updated.Metadata["key with space"])
	assert.Equal(t, "quoted", updated.Metadata["key\"quote"])
	assert.Equal(t, "slashed", updated.Metadata["key\\slash"])
	assert.Equal(t, map[string]interface{}{"state": "$pending"}, updated.Metadata["$status"])

	beforeNoOp := updated.UpdatedAt
	updated, err = store.UpdateConversation(ctx, "user1", conv.ID, nil, registrystore.MetadataPatch{})
	require.NoError(t, err)
	assert.Equal(t, beforeNoOp, updated.UpdatedAt)
	assert.Equal(t, "dotted", updated.Metadata["a.b"])

	updated, err = store.UpdateConversation(ctx, "user1", conv.ID, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, beforeNoOp, updated.UpdatedAt)
	assert.Equal(t, "dotted", updated.Metadata["a.b"])

	updated, err = store.UpdateConversation(ctx, "user1", conv.ID, nil, registrystore.MetadataPatch{
		"a.b":        nil,
		"key\"quote": nil,
		"$status":    nil,
	})
	require.NoError(t, err)
	assert.NotContains(t, updated.Metadata, "a.b")
	assert.NotContains(t, updated.Metadata, "key\"quote")
	assert.NotContains(t, updated.Metadata, "$status")
	assert.Equal(t, "spaced", updated.Metadata["key with space"])
	assert.Equal(t, "slashed", updated.Metadata["key\\slash"])
	assert.Equal(t, "keep", updated.Metadata["unrelated"])
}
