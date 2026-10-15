//go:build !nopostgresql

package postgres

import (
	"github.com/chirino/memory-service/internal/testutil/appendtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOrderedAppendBatch(t *testing.T) {
	store, ctx := setupPostgresOutboxStore(t)
	appendtest.OrderedBatch(t, ctx, store)
}

func TestAppendBatchMigrationPreservesVersionTwoEntries(t *testing.T) {
	store, ctx := setupPostgresOutboxStore(t)
	conv, err := store.CreateConversation(ctx, "user1", "client1", "Upgrade", nil, nil, nil, nil)
	require.NoError(t, err)
	require.NoError(t, store.db.Exec("ALTER TABLE entries DROP COLUMN append_batch").Error)
	id := uuid.New()
	require.NoError(t, store.db.Exec("INSERT INTO entries(id,conversation_id,conversation_group_id,channel,content_type,content) VALUES (?,?,?,'history','history',?)", id, conv.ID, conv.ConversationGroupID, []byte(`[]`)).Error)
	require.NoError(t, (&postgresMigrator{}).Migrate(ctx))
	require.NoError(t, (&postgresMigrator{}).Migrate(ctx))
	var count int64
	require.NoError(t, store.db.Raw("SELECT COUNT(*) FROM entries WHERE id=? AND append_batch IS NULL", id).Scan(&count).Error)
	require.EqualValues(t, 1, count)
	var version string
	require.NoError(t, store.db.Raw("SELECT value FROM schema_metadata WHERE key='core_schema_version'").Scan(&version).Error)
	require.Equal(t, "2", version)
}
