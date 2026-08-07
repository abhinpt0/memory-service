//go:build !nosqlite

package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/chirino/memory-service/internal/config"
	"github.com/chirino/memory-service/internal/model"
	registrymigrate "github.com/chirino/memory-service/internal/registry/migrate"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSQLiteMetadataFilterLatestMatchingFork(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		DatastoreType: "sqlite", DBURL: filepath.Join(t.TempDir(), "memory.db"),
		DatastoreMigrateAtStart: true,
	}
	ctx := config.WithContext(context.Background(), cfg)
	require.NoError(t, registrymigrate.RunAll(ctx))
	loader, err := registrystore.Select("sqlite")
	require.NoError(t, err)
	store, err := loader(ctx)
	require.NoError(t, err)

	var root *registrystore.ConversationDetail
	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		var createErr error
		root, createErr = store.CreateConversationWithID(txCtx, "user1", "", "00000000-0000-4000-8000-000000000001", "Root", map[string]interface{}{"match": "yes"}, nil, nil, nil)
		return createErr
	})
	require.NoError(t, err)

	var entries []model.Entry
	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		var appendErr error
		entries, appendErr = store.AppendEntries(txCtx, "user1", root.ID, []registrystore.CreateEntryRequest{{
			Content: json.RawMessage(`"history entry"`), ContentType: "text/plain", Channel: "history",
		}}, nil, nil, nil)
		return appendErr
	})
	require.NoError(t, err)
	require.Len(t, entries, 1)

	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		_, createErr := store.CreateConversationWithID(txCtx, "user1", "", "ffffffff-ffff-4fff-bfff-ffffffffffff", "Fork", map[string]interface{}{"match": "no"}, nil, &root.ID, &entries[0].ID)
		return createErr
	})
	require.NoError(t, err)

	filter := &registrystore.MetadataKeyFilter{Key: "match", Value: "yes"}
	var public []registrystore.ConversationSummary
	err = store.InReadTx(ctx, func(txCtx context.Context) error {
		var listErr error
		public, _, listErr = store.ListConversations(txCtx, "user1", nil, nil, 10, model.ListModeLatestFork, model.ConversationAncestryRoots, registrystore.ArchiveFilterExclude, filter)
		return listErr
	})
	require.NoError(t, err)
	require.Len(t, public, 1)
	assert.Equal(t, root.ID, public[0].ID)

	var admin []registrystore.ConversationSummary
	err = store.InReadTx(ctx, func(txCtx context.Context) error {
		var listErr error
		admin, _, listErr = store.AdminListConversations(txCtx, registrystore.AdminConversationQuery{
			Mode: model.ListModeLatestFork, Ancestry: model.ConversationAncestryRoots,
			Archived: registrystore.ArchiveFilterExclude, MetadataFilter: filter, Limit: 10,
		})
		return listErr
	})
	require.NoError(t, err)
	require.Len(t, admin, 1)
	assert.Equal(t, root.ID, admin[0].ID)
}

func TestSQLiteMetadataFilterMatchesOnlyScalarStrings(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		DatastoreType: "sqlite", DBURL: filepath.Join(t.TempDir(), "memory.db"),
		DatastoreMigrateAtStart: true,
	}
	ctx := config.WithContext(context.Background(), cfg)
	require.NoError(t, registrymigrate.RunAll(ctx))
	loader, err := registrystore.Select("sqlite")
	require.NoError(t, err)
	store, err := loader(ctx)
	require.NoError(t, err)

	var stringConv *registrystore.ConversationDetail
	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		var createErr error
		stringConv, createErr = store.CreateConversationWithID(txCtx, "user1", "", "00000000-0000-4000-8000-000000000001", "String", map[string]interface{}{"kind": "1"}, nil, nil, nil)
		return createErr
	})
	require.NoError(t, err)
	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		_, createErr := store.CreateConversationWithID(txCtx, "user1", "", "00000000-0000-4000-8000-000000000002", "Array", map[string]interface{}{"kind": []interface{}{"1"}}, nil, nil, nil)
		return createErr
	})
	require.NoError(t, err)

	filter := &registrystore.MetadataKeyFilter{Key: "kind", Value: "1"}
	var public []registrystore.ConversationSummary
	err = store.InReadTx(ctx, func(txCtx context.Context) error {
		var listErr error
		public, _, listErr = store.ListConversations(txCtx, "user1", nil, nil, 10, model.ListModeAll, model.ConversationAncestryAll, registrystore.ArchiveFilterExclude, filter)
		return listErr
	})
	require.NoError(t, err)
	require.Len(t, public, 1)
	assert.Equal(t, stringConv.ID, public[0].ID)

	var admin []registrystore.ConversationSummary
	err = store.InReadTx(ctx, func(txCtx context.Context) error {
		var listErr error
		admin, _, listErr = store.AdminListConversations(txCtx, registrystore.AdminConversationQuery{
			Mode: model.ListModeAll, Ancestry: model.ConversationAncestryAll,
			Archived: registrystore.ArchiveFilterExclude, MetadataFilter: filter, Limit: 10,
		})
		return listErr
	})
	require.NoError(t, err)
	require.Len(t, admin, 1)
	assert.Equal(t, stringConv.ID, admin[0].ID)
}
