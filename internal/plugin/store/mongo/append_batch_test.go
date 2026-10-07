//go:build !nomongo

package mongo

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/chirino/memory-service/internal/model"
	_ "github.com/chirino/memory-service/internal/plugin/cache/local"
	registrycache "github.com/chirino/memory-service/internal/registry/cache"
	registrymigrate "github.com/chirino/memory-service/internal/registry/migrate"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
	"github.com/chirino/memory-service/internal/testutil/appendtest"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestOrderedAppendBatch(t *testing.T) {
	for _, atomic := range []bool{false, true} {
		t.Run(fmt.Sprintf("atomic=%t", atomic), func(t *testing.T) {
			ctx, store := newMongoOutboxTestStore(t)
			store.cfg.OutboxEnabled = atomic
			require.NoError(t, registrymigrate.RunAll(ctx))
			appendtest.OrderedBatch(t, ctx, store)
		})
	}
}

func TestPartialAppendBatchConcurrentRecovery(t *testing.T) {
	ctx, store := newMongoOutboxTestStore(t)
	store.cfg.OutboxEnabled = false
	require.NoError(t, registrymigrate.RunAll(ctx))
	client := "client1"
	conv, err := store.CreateConversation(ctx, "user1", client, "Partial", nil, nil, nil, nil)
	require.NoError(t, err)
	seq := uint32(10)
	request := registrystore.SequencedAppendRequest{UserID: "user1", ClientID: &client, Entries: []registrystore.CreateEntryRequest{
		{Channel: "history", ContentType: "history", Content: []byte(`[{"role":"USER","text":"prefix"}]`)},
		{Channel: "journal", ContentType: "test", Seq: &seq, Content: []byte(`{"text":"anchor"}`)},
		{Channel: "history", ContentType: "history", Content: []byte(`[{"role":"USER","text":"suffix"}]`)},
	}}
	entries, err := store.AppendEntries(ctx, "user1", conv.ID, request.Entries, &client, nil, nil)
	require.NoError(t, err)
	// Simulate a process crash immediately after the sequence anchor was persisted.
	_, err = store.entries().UpdateOne(ctx, bson.M{"_id": entries[1].ID.String()}, bson.M{"$unset": bson.M{"append_batch_complete": ""}})
	require.NoError(t, err)
	_, err = store.entries().DeleteMany(ctx, bson.M{"_id": bson.M{"$in": bson.A{entries[0].ID.String(), entries[2].ID.String()}}})
	require.NoError(t, err)
	// Later appends must not affect the original range of reserved timestamps.
	_, err = store.AppendEntries(ctx, "user1", conv.ID, []registrystore.CreateEntryRequest{{Channel: "history", ContentType: "history", Content: []byte(`[]`)}}, &client, nil, nil)
	require.NoError(t, err)
	shortened := request
	shortened.Entries = shortened.Entries[:2]
	match, err := registrystore.FindSequencedAppendMatch(ctx, store, conv.ID, shortened)
	require.NoError(t, err)
	require.False(t, match.Exact)
	count, err := store.entries().CountDocuments(ctx, bson.M{"conversation_id": conv.ID})
	require.NoError(t, err)
	require.EqualValues(t, 2, count)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	created := make(chan []model.Entry, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			match, err := registrystore.FindSequencedAppendMatch(ctx, store, conv.ID, request)
			if err == nil {
				if !match.Exact || len(match.Entries) != 3 {
					err = fmt.Errorf("partial retry did not complete")
				} else {
					for i, entry := range match.Entries {
						if entry.ID != entries[i].ID || !entry.CreatedAt.Equal(entries[i].CreatedAt) {
							err = fmt.Errorf("reserved position changed")
						}
					}
				}
			}
			created <- match.Created
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	close(created)
	for err := range errs {
		require.NoError(t, err)
	}
	createdCount := 0
	for batch := range created {
		createdCount += len(batch)
	}
	require.Equal(t, 3, createdCount)
	count, err = store.entries().CountDocuments(ctx, bson.M{"conversation_id": conv.ID})
	require.NoError(t, err)
	require.EqualValues(t, 4, count)
	// Reload the service object to prove recovery uses only durable state.
	loader, err := registrystore.Select("mongo")
	require.NoError(t, err)
	reopened, err := loader(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.(*MongoStore).client.Disconnect(context.Background()) })
	match, err = registrystore.FindSequencedAppendMatch(ctx, reopened, conv.ID, request)
	require.NoError(t, err)
	require.True(t, match.Exact)
	require.Empty(t, match.Created)
}

func TestPartialAppendBatchRefreshesContextCache(t *testing.T) {
	ctx, store := newMongoOutboxTestStore(t)
	store.cfg.OutboxEnabled = false
	require.NoError(t, registrymigrate.RunAll(ctx))
	loader, err := registrycache.Select("local")
	require.NoError(t, err)
	cache, err := loader(ctx)
	require.NoError(t, err)
	store.entriesCache = cache
	client := "client1"
	conv, err := store.CreateConversation(ctx, "user1", client, "Partial context", nil, nil, nil, nil)
	require.NoError(t, err)
	old := registrystore.CreateEntryRequest{Channel: "context", ContentType: "test", Content: []byte(`["old"]`)}
	first, err := store.SyncAgentEntry(ctx, "user1", conv.ID, old, client, nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, *first.Epoch)
	cached, err := cache.Get(ctx, conv.ID, scopedAgentCacheKey(client, ""))
	require.NoError(t, err)
	require.NotNil(t, cached)
	seq, epoch := uint32(10), int64(2)
	request := registrystore.SequencedAppendRequest{UserID: "user1", ClientID: &client, Epoch: &epoch, Entries: []registrystore.CreateEntryRequest{
		{Channel: "context", ContentType: "test", Seq: &seq, Content: []byte(`["anchor"]`)},
		{Channel: "context", ContentType: "test", Content: []byte(`["new"]`)},
	}}
	entries, err := store.AppendEntries(ctx, "user1", conv.ID, request.Entries, &client, nil, &epoch)
	require.NoError(t, err)
	_, err = store.entries().UpdateOne(ctx, bson.M{"_id": entries[0].ID.String()}, bson.M{"$unset": bson.M{"append_batch_complete": ""}})
	require.NoError(t, err)
	_, err = store.entries().DeleteOne(ctx, bson.M{"_id": entries[1].ID.String()})
	require.NoError(t, err)
	// Restore the cache state from before the writer persisted its anchor.
	require.NoError(t, cache.Set(ctx, conv.ID, scopedAgentCacheKey(client, ""), *cached, 0))
	anchors, err := store.GetEntriesBySequence(ctx, "user1", conv.ID, []uint32{seq})
	require.NoError(t, err)
	require.Len(t, anchors, 1)
	match, err := registrystore.FindSequencedAppendMatch(ctx, store, conv.ID, request)
	require.NoError(t, err)
	require.True(t, match.Exact)
	refreshed, err := cache.Get(ctx, conv.ID, scopedAgentCacheKey(client, ""))
	require.NoError(t, err)
	if refreshed != nil {
		require.EqualValues(t, 2, *refreshed.Epoch)
	}
	// Another recovery loaded the incomplete anchor before the winning finalizer.
	// It must invalidate even if it loses the completion compare-and-swap.
	require.NoError(t, cache.Set(ctx, conv.ID, scopedAgentCacheKey(client, ""), *cached, 0))
	match, err = store.RecoverAppendBatch(ctx, anchors[0], request)
	require.NoError(t, err)
	require.True(t, match.Exact)
	require.Empty(t, match.Created)
	result, err := store.SyncAgentEntry(ctx, "user1", conv.ID, old, client, nil)
	require.NoError(t, err)
	require.False(t, result.NoOp, "recovered epoch 2 must replace the cached epoch 1")
	require.EqualValues(t, 3, *result.Epoch)
}
