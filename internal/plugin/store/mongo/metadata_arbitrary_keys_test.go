//go:build !nomongo

package mongo

import (
	"context"
	"fmt"
	"testing"

	"github.com/chirino/memory-service/internal/config"
	registrymigrate "github.com/chirino/memory-service/internal/registry/migrate"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
	"github.com/chirino/memory-service/internal/testutil/testmongo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMongoMetadataArbitraryKeys(t *testing.T) {
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

	// Create conversation with base metadata including unrelated keys
	conv, err := store.CreateConversation(ctx, "user1", "client1", "Test", map[string]interface{}{
		"normal":    "value",
		"unrelated": "keep",
	}, nil, nil, nil)
	require.NoError(t, err)

	// Patch with risky values: dotted key "a.b" with value starting with "$",
	// and key "$status" containing a nested object with a "$" value
	_, err = store.UpdateConversation(ctx, "user1", conv.ID, nil, map[string]interface{}{
		"a.b": "$dotted-value",
		"$status": map[string]interface{}{
			"state": "$pending",
		},
	})
	require.NoError(t, err)

	// Verify "a.b" equals "$dotted-value"
	result, err := store.GetConversation(ctx, "user1", conv.ID)
	require.NoError(t, err)
	normalized := normalizeMetadata(t, result.Metadata)

	assert.Equal(t, "value", normalized["normal"])
	assert.Equal(t, "keep", normalized["unrelated"])
	assert.Equal(t, "$dotted-value", normalized["a.b"])

	// Normalize and assert "$status" is a map whose "state" equals "$pending"
	statusMap, ok := normalized["$status"].(map[string]interface{})
	require.True(t, ok, "$status should be a map")
	assert.Equal(t, "$pending", statusMap["state"])

	// Empty-object and null metadata patches are no-ops, including updated_at.
	beforeNoOp := result.UpdatedAt
	result, err = store.UpdateConversation(ctx, "user1", conv.ID, nil, registrystore.MetadataPatch{})
	require.NoError(t, err)
	assert.Equal(t, beforeNoOp, result.UpdatedAt)
	normalized = normalizeMetadata(t, result.Metadata)
	assert.Equal(t, "$dotted-value", normalized["a.b"])

	result, err = store.UpdateConversation(ctx, "user1", conv.ID, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, beforeNoOp, result.UpdatedAt)
	normalized = normalizeMetadata(t, result.Metadata)
	assert.Equal(t, "$dotted-value", normalized["a.b"])

	// Delete BOTH "a.b" and "$status" with nil in one patch; assert both absent and normal/unrelated preserved
	_, err = store.UpdateConversation(ctx, "user1", conv.ID, nil, map[string]interface{}{
		"a.b":     nil,
		"$status": nil,
	})
	require.NoError(t, err)

	result, err = store.GetConversation(ctx, "user1", conv.ID)
	require.NoError(t, err)
	normalized = normalizeMetadata(t, result.Metadata)

	assert.NotContains(t, normalized, "a.b")
	assert.NotContains(t, normalized, "$status")
	assert.Equal(t, "value", normalized["normal"])
	assert.Equal(t, "keep", normalized["unrelated"])

	// Test constant expression depth with 150 keys in a single patch
	largePatch := make(map[string]interface{})
	for i := 0; i < 150; i++ {
		largePatch[fmt.Sprintf("key%03d", i)] = fmt.Sprintf("value%03d", i)
	}

	_, err = store.UpdateConversation(ctx, "user1", conv.ID, nil, largePatch)
	require.NoError(t, err)

	// Verify representative first/middle/last values and preservation
	result, err = store.GetConversation(ctx, "user1", conv.ID)
	require.NoError(t, err)
	normalized = normalizeMetadata(t, result.Metadata)

	assert.Equal(t, "value000", normalized["key000"])
	assert.Equal(t, "value075", normalized["key075"])
	assert.Equal(t, "value149", normalized["key149"])

	// Verify original keys are still preserved
	assert.Equal(t, "value", normalized["normal"])
	assert.Equal(t, "keep", normalized["unrelated"])
}
