package appendtest

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/chirino/memory-service/internal/model"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
	"github.com/stretchr/testify/require"
)

// OrderedBatch exercises the same persistence, retry, and fork invariants on
// every store. The caller supplies a fresh datastore.
func OrderedBatch(t *testing.T, ctx context.Context, store registrystore.MemoryStore) (string, registrystore.SequencedAppendRequest) {
	t.Helper()
	client := "client1"
	user := "user1"
	var conv *registrystore.ConversationDetail
	require.NoError(t, store.InWriteTx(ctx, func(tx context.Context) error {
		var err error
		conv, err = store.CreateConversation(tx, user, client, "Batch", nil, nil, nil, nil)
		return err
	}))
	seq10, seq11 := uint32(10), uint32(11)
	request := registrystore.SequencedAppendRequest{UserID: user, ClientID: &client, Entries: []registrystore.CreateEntryRequest{
		{Channel: "journal", ContentType: "test", Seq: &seq10, Content: []byte(`{"text":"J10"}`)},
		{Channel: "history", ContentType: "history", Content: []byte(`[{"role":"USER","text":"H10"}]`)},
		{Channel: "journal", ContentType: "test", Seq: &seq11, Content: []byte(`{"text":"J11"}`)},
		{Channel: "history", ContentType: "history", Content: []byte(`[{"role":"USER","text":"H11"}]`)},
	}}
	appendEntries := func(r registrystore.SequencedAppendRequest) ([]model.Entry, error) {
		var entries []model.Entry
		err := store.InWriteTx(ctx, func(tx context.Context) error {
			var err error
			entries, err = store.AppendEntries(tx, user, conv.ID, r.Entries, &client, nil, nil)
			return err
		})
		return entries, err
	}
	entries, err := appendEntries(request)
	require.NoError(t, err)
	require.Len(t, entries, 4)
	for i := 1; i < len(entries); i++ {
		require.True(t, entries[i].CreatedAt.After(entries[i-1].CreatedAt))
	}
	require.Nil(t, entries[1].Seq)
	require.Nil(t, entries[3].Seq)
	checkPage := func(id string, q registrystore.EntryListQuery, expected []model.Entry) {
		require.NoError(t, store.InReadTx(ctx, func(tx context.Context) error {
			page, err := store.GetEntries(tx, user, id, q)
			if err != nil {
				return err
			}
			require.Len(t, page.Data, len(expected))
			for i, entry := range page.Data {
				require.Equal(t, expected[i].ID, entry.ID)
				require.True(t, expected[i].CreatedAt.Equal(entry.CreatedAt))
			}
			return nil
		}))
	}
	checkPage(conv.ID, registrystore.EntryListQuery{Limit: 100, ClientID: &client}, entries)
	after := entries[1].ID.String()
	checkPage(conv.ID, registrystore.EntryListQuery{Limit: 2, ClientID: &client, AfterCursor: &after}, entries[2:])
	upTo := entries[2].ID.String()
	checkPage(conv.ID, registrystore.EntryListQuery{Limit: 100, ClientID: &client, UpToEntryID: &upTo}, entries[:3])
	var fork *registrystore.ConversationDetail
	require.NoError(t, store.InWriteTx(ctx, func(tx context.Context) error {
		var err error
		fork, err = store.CreateConversation(tx, user, client, "Fork", nil, nil, &conv.ID, &entries[2].ID)
		return err
	}))
	checkPage(fork.ID, registrystore.EntryListQuery{Limit: 100, ClientID: &client}, entries[:2])
	later := request
	later.Entries = []registrystore.CreateEntryRequest{{Channel: "history", ContentType: "history", Content: []byte(`[{"role":"USER","text":"later"}]`)}}
	laterEntries, err := appendEntries(later)
	require.NoError(t, err)
	require.True(t, laterEntries[0].CreatedAt.After(entries[3].CreatedAt))
	retry := func(r registrystore.SequencedAppendRequest) (registrystore.SequencedAppendMatch, error) {
		_, err := appendEntries(r)
		if !registrystore.IsDuplicateSequenceConflict(err) {
			return registrystore.SequencedAppendMatch{}, fmt.Errorf("expected sequence conflict, got %v", err)
		}
		var match registrystore.SequencedAppendMatch
		err = store.InWriteTx(ctx, func(tx context.Context) error {
			var err error
			match, err = registrystore.FindSequencedAppendMatch(tx, store, conv.ID, r)
			return err
		})
		return match, err
	}
	match, err := retry(request)
	require.NoError(t, err)
	require.True(t, match.Exact)
	require.Empty(t, match.Created)
	for i, entry := range match.Entries {
		require.Equal(t, entries[i].ID, entry.ID)
		require.True(t, entries[i].CreatedAt.Equal(entry.CreatedAt))
	}
	for _, variant := range []string{"content", "order", "shorter", "longer"} {
		changed := request
		changed.Entries = append([]registrystore.CreateEntryRequest(nil), request.Entries...)
		switch variant {
		case "content":
			changed.Entries[3].Content = []byte(`[{"role":"USER","text":"changed"}]`)
		case "order":
			changed.Entries[1], changed.Entries[3] = changed.Entries[3], changed.Entries[1]
		case "shorter":
			changed.Entries = changed.Entries[:3]
		case "longer":
			changed.Entries = append(changed.Entries, later.Entries[0])
		}
		match, err := retry(changed)
		require.NoError(t, err, variant)
		require.False(t, match.Exact, variant)
	}
	// Retrying just the anchor must not turn a completed batch into a shorter one.
	single := request
	single.Entries = single.Entries[:1]
	match, err = retry(single)
	require.NoError(t, err)
	require.False(t, match.Exact)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			match, err := retry(request)
			if err == nil && (!match.Exact || len(match.Created) > 0) {
				err = fmt.Errorf("concurrent completed retry did not match")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	checkPage(conv.ID, registrystore.EntryListQuery{Limit: 100, ClientID: &client}, append(entries, laterEntries...))
	// Concurrent appends reserve complete, disjoint ranges after the committed tail.
	batches := make(chan []model.Entry, 8)
	appendErrs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			concurrent := later
			concurrent.Entries = append([]registrystore.CreateEntryRequest(nil), later.Entries...)
			concurrent.Entries = append(concurrent.Entries, concurrent.Entries[0])
			batch, err := appendEntries(concurrent)
			batches <- batch
			appendErrs <- err
		}()
	}
	wg.Wait()
	close(batches)
	close(appendErrs)
	for err := range appendErrs {
		require.NoError(t, err)
	}
	all := append([]model.Entry(nil), entries...)
	all = append(all, laterEntries...)
	for batch := range batches {
		require.Len(t, batch, 2)
		require.True(t, batch[0].CreatedAt.After(laterEntries[0].CreatedAt))
		require.True(t, batch[1].CreatedAt.After(batch[0].CreatedAt))
		all = append(all, batch...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.Before(all[j].CreatedAt) })
	for i := 1; i < len(all); i++ {
		require.True(t, all[i].CreatedAt.After(all[i-1].CreatedAt))
	}
	checkPage(conv.ID, registrystore.EntryListQuery{Limit: 100, ClientID: &client}, all)
	return conv.ID, request
}
