//go:build !nomongo

package mongo

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/chirino/memory-service/internal/config"
	"github.com/chirino/memory-service/internal/model"
	registrymigrate "github.com/chirino/memory-service/internal/registry/migrate"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
	"github.com/chirino/memory-service/internal/testutil/testmongo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMongoMetadataFilterLatestMatchingFork(t *testing.T) {
	dbURL := testmongo.StartMongo(t)
	cfg := config.DefaultConfig()
	cfg.DBURL = dbURL
	cfg.DatastoreType = "mongo"
	ctx := config.WithContext(context.Background(), &cfg)
	require.NoError(t, registrymigrate.RunAll(ctx))
	loader, err := registrystore.Select("mongo")
	require.NoError(t, err)
	store, err := loader(ctx)
	require.NoError(t, err)

	root, err := store.CreateConversationWithID(ctx, "user1", "", "00000000-0000-4000-8000-000000000001", "Root", map[string]interface{}{"match": "yes"}, nil, nil, nil)
	require.NoError(t, err)
	entries, err := store.AppendEntries(ctx, "user1", root.ID, []registrystore.CreateEntryRequest{{
		Content: json.RawMessage(`"history entry"`), ContentType: "text/plain", Channel: "history",
	}}, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	_, err = store.CreateConversationWithID(ctx, "user1", "", "ffffffff-ffff-4fff-bfff-ffffffffffff", "Fork", map[string]interface{}{"match": "no"}, nil, &root.ID, &entries[0].ID)
	require.NoError(t, err)

	filter := &registrystore.MetadataKeyFilter{Key: "match", Value: "yes"}
	public, _, err := store.ListConversations(ctx, "user1", nil, nil, 10, model.ListModeLatestFork, model.ConversationAncestryRoots, registrystore.ArchiveFilterExclude, filter)
	require.NoError(t, err)
	require.Len(t, public, 1)
	assert.Equal(t, root.ID, public[0].ID)

	admin, _, err := store.AdminListConversations(ctx, registrystore.AdminConversationQuery{
		Mode: model.ListModeLatestFork, Ancestry: model.ConversationAncestryRoots,
		Archived: registrystore.ArchiveFilterExclude, MetadataFilter: filter, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, admin, 1)
	assert.Equal(t, root.ID, admin[0].ID)
}

func TestMongoMetadataFilterMatchesOnlyScalarStrings(t *testing.T) {
	dbURL := testmongo.StartMongo(t)
	cfg := config.DefaultConfig()
	cfg.DBURL = dbURL
	cfg.DatastoreType = "mongo"
	ctx := config.WithContext(context.Background(), &cfg)
	require.NoError(t, registrymigrate.RunAll(ctx))
	loader, err := registrystore.Select("mongo")
	require.NoError(t, err)
	store, err := loader(ctx)
	require.NoError(t, err)

	stringConv, err := store.CreateConversationWithID(ctx, "user1", "", "00000000-0000-4000-8000-000000000001", "String", map[string]interface{}{"kind": "1"}, nil, nil, nil)
	require.NoError(t, err)
	_, err = store.CreateConversationWithID(ctx, "user1", "", "00000000-0000-4000-8000-000000000002", "Array", map[string]interface{}{"kind": []interface{}{"1"}}, nil, nil, nil)
	require.NoError(t, err)

	filter := &registrystore.MetadataKeyFilter{Key: "kind", Value: "1"}
	public, _, err := store.ListConversations(ctx, "user1", nil, nil, 10, model.ListModeAll, model.ConversationAncestryAll, registrystore.ArchiveFilterExclude, filter)
	require.NoError(t, err)
	require.Len(t, public, 1)
	assert.Equal(t, stringConv.ID, public[0].ID)

	admin, _, err := store.AdminListConversations(ctx, registrystore.AdminConversationQuery{
		Mode: model.ListModeAll, Ancestry: model.ConversationAncestryAll,
		Archived: registrystore.ArchiveFilterExclude, MetadataFilter: filter, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, admin, 1)
	assert.Equal(t, stringConv.ID, admin[0].ID)
}
