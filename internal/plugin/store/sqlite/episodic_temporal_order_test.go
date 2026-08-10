//go:build !nosqlite && sqlite_fts5

package sqlite

import (
	"context"
	"testing"

	registryepisodic "github.com/chirino/memory-service/internal/registry/episodic"
	"github.com/stretchr/testify/require"
)

// TestSQLiteTemporalAttributeRangeOrder verifies that $gte/$lte range filters on
// observedAt return memories in correct chronological order on SQLite.
func TestSQLiteTemporalAttributeRangeOrder(t *testing.T) {
	t.Parallel()

	store, ctx := newSQLiteEpisodicStore(t)

	ns := []string{"user", "alice", "cognition.v1", "facts"}

	const tsEarlier = "2025-06-10T13:30:00.000000000Z"
	const tsLater = "2025-06-10T13:30:01.000000000Z"

	require.NoError(t, store.InWriteTx(ctx, func(wctx context.Context) error {
		_, err := store.PutMemory(wctx, registryepisodic.PutMemoryRequest{
			Namespace:        ns,
			Key:              "mem-earlier",
			Value:            map[string]interface{}{"statement": "earlier fact"},
			PolicyAttributes: map[string]interface{}{"observedAt": tsEarlier},
		})
		if err != nil {
			return err
		}
		_, err = store.PutMemory(wctx, registryepisodic.PutMemoryRequest{
			Namespace:        ns,
			Key:              "mem-later",
			Value:            map[string]interface{}{"statement": "later fact"},
			PolicyAttributes: map[string]interface{}{"observedAt": tsLater},
		})
		return err
	}))

	require.NoError(t, store.InReadTx(ctx, func(rctx context.Context) error {
		// $gte tsLater must match only mem-later.
		filter, err := registryepisodic.NormalizeAttributeFilters(map[string]interface{}{
			"observedAt": map[string]interface{}{"$gte": tsLater},
		})
		require.NoError(t, err)

		items, err := store.SearchMemories(rctx, ns, filter, 10, registryepisodic.ArchiveFilterExclude)
		require.NoError(t, err)
		require.Len(t, items, 1, "expected only the later memory to match $gte tsLater")
		require.Equal(t, "mem-later", items[0].Key)

		// $lte tsEarlier must match only mem-earlier.
		filter2, err := registryepisodic.NormalizeAttributeFilters(map[string]interface{}{
			"observedAt": map[string]interface{}{"$lte": tsEarlier},
		})
		require.NoError(t, err)

		items2, err := store.SearchMemories(rctx, ns, filter2, 10, registryepisodic.ArchiveFilterExclude)
		require.NoError(t, err)
		require.Len(t, items2, 1, "expected only the earlier memory to match $lte tsEarlier")
		require.Equal(t, "mem-earlier", items2[0].Key)
		return nil
	}))
}
