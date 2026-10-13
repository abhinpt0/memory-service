//go:build !nosqlite

package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chirino/memory-service/internal/config"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
	"github.com/chirino/memory-service/internal/testutil/appendtest"
	"github.com/stretchr/testify/require"
)

func TestOrderedAppendBatch(t *testing.T) {
	cfg := &config.Config{DatastoreType: "sqlite", DBURL: filepath.Join(t.TempDir(), "batch.db"), DatastoreMigrateAtStart: true, EncryptionDBDisabled: true}
	ctx := config.WithContext(context.Background(), cfg)
	require.NoError(t, (&sqliteMigrator{}).Migrate(ctx))
	loader, err := registrystore.Select("sqlite")
	require.NoError(t, err)
	store, err := loader(ctx)
	require.NoError(t, err)
	concrete := store.(*SQLiteStore)
	concrete.enc = newCheckpointEncryptionService(t)
	conversationID, request := appendtest.OrderedBatch(t, ctx, store)
	sharedHandles.Lock()
	handle := sharedHandles.byDSN[cfg.DBURL]
	delete(sharedHandles.byDSN, cfg.DBURL)
	sharedHandles.Unlock()
	require.NoError(t, handle.sqlDB.Close())
	require.NoError(t, (&sqliteMigrator{}).Migrate(ctx))
	reopened, err := loader(ctx)
	require.NoError(t, err)
	reopened.(*SQLiteStore).enc = newCheckpointEncryptionService(t)
	require.NoError(t, reopened.InWriteTx(ctx, func(tx context.Context) error {
		match, err := registrystore.FindSequencedAppendMatch(tx, reopened, conversationID, request)
		require.NoError(t, err)
		require.True(t, match.Exact)
		require.Empty(t, match.Created)
		return nil
	}))
}

func TestAppendBatchMigrationPreservesVersionTwoEntries(t *testing.T) {
	cfg := &config.Config{DatastoreType: "sqlite", DBURL: filepath.Join(t.TempDir(), "upgrade.db"), DatastoreMigrateAtStart: true, EncryptionDBDisabled: true}
	ctx := config.WithContext(context.Background(), cfg)
	handle, err := getSharedHandle(ctx)
	require.NoError(t, err)
	oldSchema := strings.Replace(schemaSQL, "    append_batch BLOB,\n", "", 1)
	require.NotEqual(t, schemaSQL, oldSchema)
	_, err = handle.sqlDB.ExecContext(ctx, oldSchema)
	require.NoError(t, err)
	_, err = handle.sqlDB.ExecContext(ctx, `INSERT INTO conversation_groups(id) VALUES ('group1'); INSERT INTO conversations(id,conversation_group_id,owner_user_id,client_id) VALUES ('conv1','group1','user1','client1'); INSERT INTO entries(id,conversation_id,conversation_group_id,channel,content_type,content) VALUES ('entry1','conv1','group1','history','history','[]')`)
	require.NoError(t, err)
	require.NoError(t, (&sqliteMigrator{}).Migrate(ctx))
	require.NoError(t, (&sqliteMigrator{}).Migrate(ctx))
	var count int
	require.NoError(t, handle.sqlDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM entries WHERE id='entry1' AND append_batch IS NULL").Scan(&count))
	require.Equal(t, 1, count)
	var version string
	require.NoError(t, handle.sqlDB.QueryRowContext(ctx, "SELECT value FROM schema_metadata WHERE key='core_schema_version'").Scan(&version))
	require.Equal(t, "2", version)
}
