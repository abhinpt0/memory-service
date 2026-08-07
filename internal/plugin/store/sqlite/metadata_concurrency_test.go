//go:build !nosqlite

package sqlite

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/chirino/memory-service/internal/config"
	registrymigrate "github.com/chirino/memory-service/internal/registry/migrate"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
	"github.com/chirino/memory-service/internal/testutil/testbarrier"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSQLiteMetadataConcurrentPatch(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		DatastoreType:           "sqlite",
		DBURL:                   filepath.Join(t.TempDir(), "memory.db"),
		DatastoreMigrateAtStart: true,
	}
	ctx := config.WithContext(context.Background(), cfg)

	err := registrymigrate.RunAll(ctx)
	require.NoError(t, err)

	loader, err := registrystore.Select("sqlite")
	require.NoError(t, err)
	store, err := loader(ctx)
	require.NoError(t, err)
	sqliteStore, ok := store.(*SQLiteStore)
	require.True(t, ok)

	// Create conversation with base metadata
	var convID string
	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		conv, err := store.CreateConversation(txCtx, "user1", "client1", "Test", map[string]interface{}{
			"base": "x",
		}, nil, nil, nil)
		if err != nil {
			return err
		}
		convID = conv.ID
		return nil
	})
	require.NoError(t, err)

	// Run both calls in one write scope so they can deterministically read the same
	// pre-update metadata without SQLite's process-local write mutex serializing them.
	barrier := testbarrier.New(2)
	sqliteStore.metadataPatchBeforeUpdate = barrier.Wait
	t.Cleanup(func() { sqliteStore.metadataPatchBeforeUpdate = nil })
	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		wg.Add(2)

		go func() {
			defer wg.Done()
			_, err := store.UpdateConversation(txCtx, "user1", convID, nil, map[string]interface{}{
				"left": "1",
			})
			errs <- err
		}()

		go func() {
			defer wg.Done()
			_, err := store.UpdateConversation(txCtx, "user1", convID, nil, map[string]interface{}{
				"right": "2",
			})
			errs <- err
		}()

		barrier.WaitForParties(t, 2)
		wg.Wait()
		close(errs)
		for updateErr := range errs {
			if updateErr != nil {
				return updateErr
			}
		}
		return nil
	})
	require.NoError(t, err)

	// Verify all three keys remain
	var result *registrystore.ConversationDetail
	err = store.InReadTx(ctx, func(txCtx context.Context) error {
		var err error
		result, err = store.GetConversation(txCtx, "user1", convID)
		return err
	})
	require.NoError(t, err)
	assert.Equal(t, "x", result.Metadata["base"])
	assert.Equal(t, "1", result.Metadata["left"])
	assert.Equal(t, "2", result.Metadata["right"])
}

func TestSQLiteMetadataExplicitNullDeletion(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		DatastoreType:           "sqlite",
		DBURL:                   filepath.Join(t.TempDir(), "memory.db"),
		DatastoreMigrateAtStart: true,
	}
	ctx := config.WithContext(context.Background(), cfg)

	err := registrymigrate.RunAll(ctx)
	require.NoError(t, err)

	loader, err := registrystore.Select("sqlite")
	require.NoError(t, err)
	store, err := loader(ctx)
	require.NoError(t, err)

	// Create conversation with nested metadata
	var convID string
	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		conv, err := store.CreateConversation(txCtx, "user1", "client1", "Test", map[string]interface{}{
			"keep":   "value",
			"delete": "old",
			"nested": map[string]interface{}{
				"inner": nil, // stored null
			},
		}, nil, nil, nil)
		if err != nil {
			return err
		}
		convID = conv.ID
		return nil
	})
	require.NoError(t, err)

	// Patch with explicit null to delete "delete" key
	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		_, err := store.UpdateConversation(txCtx, "user1", convID, nil, map[string]interface{}{
			"delete": nil,
		})
		return err
	})
	require.NoError(t, err)

	// Verify "delete" key removed, others preserved
	var result *registrystore.ConversationDetail
	err = store.InReadTx(ctx, func(txCtx context.Context) error {
		var err error
		result, err = store.GetConversation(txCtx, "user1", convID)
		return err
	})
	require.NoError(t, err)
	assert.Equal(t, "value", result.Metadata["keep"])
	assert.NotContains(t, result.Metadata, "delete")
	// Nested structure with stored null should remain
	nested, ok := result.Metadata["nested"].(map[string]interface{})
	require.True(t, ok)
	assert.Contains(t, nested, "inner")
	assert.Nil(t, nested["inner"])
}

func TestSQLiteMetadataNestedObjectReplacement(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		DatastoreType:           "sqlite",
		DBURL:                   filepath.Join(t.TempDir(), "memory.db"),
		DatastoreMigrateAtStart: true,
	}
	ctx := config.WithContext(context.Background(), cfg)

	err := registrymigrate.RunAll(ctx)
	require.NoError(t, err)

	loader, err := registrystore.Select("sqlite")
	require.NoError(t, err)
	store, err := loader(ctx)
	require.NoError(t, err)

	// Create conversation with nested metadata
	var convID string
	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		conv, err := store.CreateConversation(txCtx, "user1", "client1", "Test", map[string]interface{}{
			"obj": map[string]interface{}{
				"a": "1",
				"b": "2",
			},
		}, nil, nil, nil)
		if err != nil {
			return err
		}
		convID = conv.ID
		return nil
	})
	require.NoError(t, err)

	// Replace entire top-level "obj" with new nested object
	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		_, err := store.UpdateConversation(txCtx, "user1", convID, nil, map[string]interface{}{
			"obj": map[string]interface{}{
				"c": "3",
			},
		})
		return err
	})
	require.NoError(t, err)

	// Verify old nested keys are gone, new nested key exists
	var result *registrystore.ConversationDetail
	err = store.InReadTx(ctx, func(txCtx context.Context) error {
		var err error
		result, err = store.GetConversation(txCtx, "user1", convID)
		return err
	})
	require.NoError(t, err)
	obj, ok := result.Metadata["obj"].(map[string]interface{})
	require.True(t, ok)
	assert.NotContains(t, obj, "a")
	assert.NotContains(t, obj, "b")
	assert.Equal(t, "3", obj["c"])
}

func TestSQLiteMetadataArbitraryKeysAndEmptyPatch(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		DatastoreType:           "sqlite",
		DBURL:                   filepath.Join(t.TempDir(), "memory.db"),
		DatastoreMigrateAtStart: true,
	}
	ctx := config.WithContext(context.Background(), cfg)

	err := registrymigrate.RunAll(ctx)
	require.NoError(t, err)

	loader, err := registrystore.Select("sqlite")
	require.NoError(t, err)
	store, err := loader(ctx)
	require.NoError(t, err)

	var convID string
	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		conv, err := store.CreateConversation(txCtx, "user1", "client1", "Test", map[string]interface{}{
			"unrelated": "keep",
		}, nil, nil, nil)
		if err != nil {
			return err
		}
		convID = conv.ID
		return nil
	})
	require.NoError(t, err)

	var updated *registrystore.ConversationDetail
	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		var err error
		updated, err = store.UpdateConversation(txCtx, "user1", convID, nil, registrystore.MetadataPatch{
			"a.b":            "dotted",
			"key with space": "spaced",
			"key\"quote":     "quoted",
			"key\\slash":     "slashed",
			"$status": map[string]interface{}{
				"state": "$pending",
			},
		})
		return err
	})
	require.NoError(t, err)
	assert.Equal(t, "keep", updated.Metadata["unrelated"])
	assert.Equal(t, "dotted", updated.Metadata["a.b"])
	assert.Equal(t, "spaced", updated.Metadata["key with space"])
	assert.Equal(t, "quoted", updated.Metadata["key\"quote"])
	assert.Equal(t, "slashed", updated.Metadata["key\\slash"])
	assert.Equal(t, map[string]interface{}{"state": "$pending"}, updated.Metadata["$status"])

	beforeNoOp := updated.UpdatedAt
	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		var err error
		updated, err = store.UpdateConversation(txCtx, "user1", convID, nil, registrystore.MetadataPatch{})
		return err
	})
	require.NoError(t, err)
	assert.Equal(t, beforeNoOp, updated.UpdatedAt)
	assert.Equal(t, "dotted", updated.Metadata["a.b"])

	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		var err error
		updated, err = store.UpdateConversation(txCtx, "user1", convID, nil, nil)
		return err
	})
	require.NoError(t, err)
	assert.Equal(t, beforeNoOp, updated.UpdatedAt)
	assert.Equal(t, "dotted", updated.Metadata["a.b"])

	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		var err error
		updated, err = store.UpdateConversation(txCtx, "user1", convID, nil, registrystore.MetadataPatch{
			"a.b":        nil,
			"key\"quote": nil,
			"$status":    nil,
		})
		return err
	})
	require.NoError(t, err)
	assert.NotContains(t, updated.Metadata, "a.b")
	assert.NotContains(t, updated.Metadata, "key\"quote")
	assert.NotContains(t, updated.Metadata, "$status")
	assert.Equal(t, "spaced", updated.Metadata["key with space"])
	assert.Equal(t, "slashed", updated.Metadata["key\\slash"])
	assert.Equal(t, "keep", updated.Metadata["unrelated"])
}
