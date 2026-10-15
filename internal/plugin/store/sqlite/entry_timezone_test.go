package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/chirino/memory-service/internal/config"
	"github.com/chirino/memory-service/internal/model"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestEntryOrderAfterTimezoneUpgrade(t *testing.T) {
	cfg := &config.Config{DatastoreType: "sqlite", DBURL: filepath.Join(t.TempDir(), "upgrade.db"), DatastoreMigrateAtStart: true, EncryptionDBDisabled: true}
	ctx := config.WithContext(context.Background(), cfg)
	require.NoError(t, (&sqliteMigrator{}).Migrate(ctx))
	loader, err := registrystore.Select("sqlite")
	require.NoError(t, err)
	store, err := loader(ctx)
	require.NoError(t, err)
	s := store.(*SQLiteStore)
	var conv *registrystore.ConversationDetail
	require.NoError(t, store.InWriteTx(ctx, func(tx context.Context) error {
		var err error
		conv, err = store.CreateConversation(tx, "alice", "client", "Upgrade", nil, nil, nil, nil)
		return err
	}))
	oldTime := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	legacy := []model.Entry{
		{ID: uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff"), CreatedAt: oldTime.Add(100 * time.Nanosecond).In(time.FixedZone("UTC+0530", 19800))},
		{ID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), CreatedAt: oldTime.Add(200 * time.Nanosecond).In(time.FixedZone("UTC-0700", -25200))},
	}
	for i := range legacy {
		legacy[i].ConversationID = conv.ID
		legacy[i].ConversationGroupID = conv.ConversationGroupID
		legacy[i].Channel = model.ChannelHistory
		legacy[i].ContentType = "history"
		legacy[i].Content = []byte(`[]`)
	}
	// Model legacy rows written before the numeric timestamp backfill.
	require.NoError(t, store.InWriteTx(ctx, func(tx context.Context) error { return s.dbFor(tx).Create(&legacy).Error }))
	require.NoError(t, (&sqliteMigrator{}).Migrate(ctx))
	var newer []model.Entry
	require.NoError(t, store.InWriteTx(ctx, func(tx context.Context) error {
		var err error
		newer, err = store.AppendEntries(tx, "alice", conv.ID, []registrystore.CreateEntryRequest{
			{Channel: "history", ContentType: "history", Content: []byte(`[]`)},
			{Channel: "history", ContentType: "history", Content: []byte(`[]`)},
		}, nil, nil, nil)
		return err
	}))
	want := append(append([]model.Entry(nil), legacy...), newer...)
	check := func(t *testing.T, id string, q registrystore.EntryListQuery, expected []model.Entry) {
		t.Helper()
		require.NoError(t, store.InReadTx(ctx, func(tx context.Context) error {
			page, err := store.GetEntries(tx, "alice", id, q)
			require.NoError(t, err)
			require.Len(t, page.Data, len(expected))
			for i := range expected {
				require.Equal(t, expected[i].ID, page.Data[i].ID)
				require.True(t, expected[i].CreatedAt.Equal(page.Data[i].CreatedAt))
			}
			return nil
		}))
	}
	t.Run("listing", func(t *testing.T) { check(t, conv.ID, registrystore.EntryListQuery{Limit: 10}, want) })
	t.Run("tail", func(t *testing.T) { check(t, conv.ID, registrystore.EntryListQuery{Limit: 2, Tail: true}, newer) })
	after, before, upTo := legacy[0].ID.String(), newer[0].ID.String(), legacy[1].ID.String()
	t.Run("after", func(t *testing.T) {
		check(t, conv.ID, registrystore.EntryListQuery{Limit: 10, AfterCursor: &after}, want[1:])
	})
	t.Run("before", func(t *testing.T) {
		check(t, conv.ID, registrystore.EntryListQuery{Limit: 10, BeforeCursor: &before}, legacy)
	})
	t.Run("upTo", func(t *testing.T) {
		check(t, conv.ID, registrystore.EntryListQuery{Limit: 10, UpToEntryID: &upTo}, legacy)
	})
	t.Run("admin", func(t *testing.T) {
		require.NoError(t, store.InReadTx(ctx, func(tx context.Context) error {
			page, err := store.AdminGetEntries(tx, conv.ID, registrystore.AdminMessageQuery{Limit: 10})
			require.NoError(t, err)
			require.Len(t, page.Data, len(want))
			for i := range want {
				require.Equal(t, want[i].ID, page.Data[i].ID)
			}
			return nil
		}))
	})
	t.Run("event-snapshot", func(t *testing.T) {
		require.NoError(t, store.InReadTx(ctx, func(tx context.Context) error {
			var cursor *registrystore.EventSnapshotEntryCursor
			for _, expected := range want {
				page, next, err := s.AdminListEventSnapshotEntries(tx, cursor, 1)
				require.NoError(t, err)
				require.Len(t, page, 1)
				require.Equal(t, expected.ID, page[0].ID)
				cursor = next
			}
			return nil
		}))
	})
	for _, boundary := range []model.Entry{legacy[1], newer[0]} {
		t.Run("fork-"+boundary.ID.String(), func(t *testing.T) {
			var fork *registrystore.ConversationDetail
			require.NoError(t, store.InWriteTx(ctx, func(tx context.Context) error {
				var err error
				fork, err = store.CreateConversation(tx, "alice", "client", "Fork", nil, nil, &conv.ID, &boundary.ID)
				return err
			}))
			expected := legacy
			if boundary.ID == legacy[1].ID {
				expected = legacy[:1]
			}
			check(t, fork.ID, registrystore.EntryListQuery{Limit: 10}, expected)
		})
	}
}
