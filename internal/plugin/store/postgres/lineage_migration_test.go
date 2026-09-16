//go:build !nopostgresql

package postgres

import (
	"context"
	"testing"

	"github.com/chirino/memory-service/internal/config"
	"github.com/chirino/memory-service/internal/testutil/testpg"
	"github.com/stretchr/testify/require"
	postgresdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresMigratorMakesStartedByConversationASoftReference(t *testing.T) {
	dbURL := testpg.StartPostgres(t)
	cfg := config.DefaultConfig()
	cfg.DatastoreType = "postgres"
	cfg.DBURL = dbURL
	cfg.DatastoreMigrateAtStart = true
	ctx := config.WithContext(context.Background(), &cfg)
	require.NoError(t, (&postgresMigrator{}).Migrate(ctx))

	db, err := gorm.Open(postgresdriver.Open(dbURL), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		ALTER TABLE conversations
			ADD CONSTRAINT conversations_started_by_conversation_id_fkey
			FOREIGN KEY (started_by_conversation_id) REFERENCES conversations(id) ON DELETE CASCADE;
		INSERT INTO conversation_groups (id)
			VALUES ('00000000-0000-4000-8000-000000000001'), ('00000000-0000-4000-8000-000000000002');
		INSERT INTO conversations (
			id, owner_user_id, client_id, conversation_group_id
		) VALUES (
			'parent-conversation', 'alice', 'test-client', '00000000-0000-4000-8000-000000000001'
		);
		INSERT INTO conversations (
			id, owner_user_id, client_id, conversation_group_id, started_by_conversation_id
		) VALUES (
			'child-conversation', 'alice', 'test-client',
			'00000000-0000-4000-8000-000000000002', 'parent-conversation'
		);
	`).Error)

	require.NoError(t, (&postgresMigrator{}).Migrate(ctx))
	require.NoError(t, db.Exec(
		"DELETE FROM conversation_groups WHERE id = ?",
		"00000000-0000-4000-8000-000000000001",
	).Error)

	var childCount int64
	require.NoError(t, db.Raw(
		"SELECT COUNT(*) FROM conversations WHERE id = ? AND started_by_conversation_id = ?",
		"child-conversation", "parent-conversation",
	).Scan(&childCount).Error)
	require.Equal(t, int64(1), childCount)

	var startedByForeignKeys int64
	require.NoError(t, db.Raw(`
		SELECT COUNT(*)
		FROM pg_constraint c
		JOIN pg_attribute a
			ON a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey)
		WHERE c.conrelid = 'conversations'::regclass
		  AND c.contype = 'f'
		  AND a.attname = 'started_by_conversation_id'
	`).Scan(&startedByForeignKeys).Error)
	require.Zero(t, startedByForeignKeys)
}
