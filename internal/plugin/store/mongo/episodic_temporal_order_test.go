//go:build !nomongo

package mongo_test

import (
	"testing"

	registryepisodic "github.com/chirino/memory-service/internal/registry/episodic"
	"github.com/stretchr/testify/require"
)

// TestMongoTemporalAttributeRangeOrder verifies that $gte/$lte range filters on
// observedAt return memories in correct chronological order on MongoDB even when
// query bounds use equivalent offsets and fractional precision.
func TestMongoTemporalAttributeRangeOrder(t *testing.T) {
	store, ctx := setupMongoEpisodicStore(t)

	ns := []string{"user", "alice", "cognition.v1", "facts"}

	const tsEarlier = "2025-06-10T13:30:00.000000000Z"
	const tsLater = "2025-06-10T13:30:01.000000000Z"
	const tsEarlierBound = "2025-06-10T13:30:00Z"
	const tsLaterBound = "2025-06-10T09:30:01-04:00"

	_, err := store.PutMemory(ctx, registryepisodic.PutMemoryRequest{
		Namespace:        ns,
		Key:              "mem-earlier",
		Value:            map[string]interface{}{"statement": "earlier fact"},
		PolicyAttributes: map[string]interface{}{"observedAt": tsEarlier},
	})
	require.NoError(t, err)

	_, err = store.PutMemory(ctx, registryepisodic.PutMemoryRequest{
		Namespace:        ns,
		Key:              "mem-later",
		Value:            map[string]interface{}{"statement": "later fact"},
		PolicyAttributes: map[string]interface{}{"observedAt": tsLater},
	})
	require.NoError(t, err)

	// $gte tsLater must match only mem-later.
	filter, err := registryepisodic.NormalizeAttributeFilters(map[string]interface{}{
		"observedAt": map[string]interface{}{"$gte": tsLaterBound},
	})
	require.NoError(t, err)

	items, err := store.SearchMemories(ctx, ns, filter, 10, registryepisodic.ArchiveFilterExclude)
	require.NoError(t, err)
	require.Len(t, items, 1, "expected only the later memory to match $gte tsLater")
	require.Equal(t, "mem-later", items[0].Key)

	// $lte tsEarlier must match only mem-earlier.
	filter2, err := registryepisodic.NormalizeAttributeFilters(map[string]interface{}{
		"observedAt": map[string]interface{}{"$lte": tsEarlierBound},
	})
	require.NoError(t, err)

	items2, err := store.SearchMemories(ctx, ns, filter2, 10, registryepisodic.ArchiveFilterExclude)
	require.NoError(t, err)
	require.Len(t, items2, 1, "expected only the earlier memory to match $lte tsEarlier")
	require.Equal(t, "mem-earlier", items2[0].Key)
}
