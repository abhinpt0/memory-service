//go:build !nosqlite

package sqlite

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chirino/memory-service/internal/config"
	"github.com/chirino/memory-service/internal/model"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAttachmentBlobDeleteTaskCommitsAndRollsBackWithWriteTransaction(t *testing.T) {
	cfg := &config.Config{
		DatastoreType:           "sqlite",
		DBURL:                   filepath.Join(t.TempDir(), "attachment-task.db"),
		DatastoreMigrateAtStart: true,
		EncryptionDBDisabled:    true,
	}
	ctx := config.WithContext(context.Background(), cfg)
	require.NoError(t, (&sqliteMigrator{}).Migrate(ctx))
	loader, err := registrystore.Select("sqlite")
	require.NoError(t, err)
	store, err := loader(ctx)
	require.NoError(t, err)

	rollbackErr := errors.New("rollback")
	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		require.NoError(t, registrystore.CreateAttachmentBlobDeleteTask(txCtx, store, "stored-key"))
		return rollbackErr
	})
	require.ErrorIs(t, err, rollbackErr)

	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		tasks, err := store.ClaimReadyTasks(txCtx, 10)
		require.NoError(t, err)
		require.Empty(t, tasks)
		return nil
	}))

	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		return registrystore.CreateAttachmentBlobDeleteTask(txCtx, store, "stored-key")
	}))
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		return registrystore.CreateAttachmentBlobDeleteTask(txCtx, store, "stored-key")
	}))

	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		tasks, err := store.ClaimReadyTasks(txCtx, 10)
		require.NoError(t, err)
		require.Len(t, tasks, 1)
		require.Equal(t, registrystore.AttachmentBlobDeleteTaskType, tasks[0].TaskType)
		require.Equal(t, "stored-key", tasks[0].TaskBody[registrystore.AttachmentBlobDeleteStorageKeyField])
		return nil
	}))
}

func TestDeleteAttachmentQueuesExactStorageKey(t *testing.T) {
	store, ctx := newAttachmentTaskSQLiteStore(t)
	storageKey := "deleted-storage-key"
	var attachment *model.Attachment
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		var err error
		attachment, err = store.CreateAttachment(txCtx, "user1", "", model.Attachment{StorageKey: &storageKey})
		return err
	}))
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		return store.DeleteAttachment(txCtx, "user1", "", attachment.ID)
	}))

	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		tasks, err := store.ClaimReadyTasks(txCtx, 1)
		if err != nil {
			return err
		}
		require.Len(t, tasks, 1)
		require.Equal(t, storageKey, tasks[0].TaskBody[registrystore.AttachmentBlobDeleteStorageKeyField])
		referenced, err := store.PrepareAttachmentBlobDelete(txCtx, tasks[0].ID, storageKey)
		require.NoError(t, err)
		require.False(t, referenced)
		return store.CompleteAttachmentBlobDelete(txCtx, tasks[0].ID)
	}))
}

func TestCreateAttachmentCancelsQueuedBlobDelete(t *testing.T) {
	store, ctx := newAttachmentTaskSQLiteStore(t)
	storageKey := "shared-key"
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		if err := registrystore.CreateAttachmentBlobDeleteTask(txCtx, store, storageKey); err != nil {
			return err
		}
		_, err := store.CreateAttachment(txCtx, "user1", "", model.Attachment{StorageKey: &storageKey})
		return err
	}))

	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		tasks, err := store.ClaimReadyTasks(txCtx, 10)
		require.NoError(t, err)
		require.Empty(t, tasks)
		return nil
	}))
}

func TestCreateAttachmentRejectsClaimedBlobDelete(t *testing.T) {
	store, ctx := newAttachmentTaskSQLiteStore(t)
	storageKey := "shared-key"
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		return registrystore.CreateAttachmentBlobDeleteTask(txCtx, store, storageKey)
	}))
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		tasks, err := store.ClaimReadyTasks(txCtx, 1)
		require.NoError(t, err)
		require.Len(t, tasks, 1)
		return store.FailTask(txCtx, tasks[0].ID, "provider unavailable", 0)
	}))

	err := store.InWriteTx(ctx, func(txCtx context.Context) error {
		_, err := store.CreateAttachment(txCtx, "user1", "", model.Attachment{StorageKey: &storageKey})
		return err
	})
	var conflict *registrystore.ConflictError
	require.ErrorAs(t, err, &conflict)
}

func TestUpdateAttachmentCoordinatesBlobDelete(t *testing.T) {
	store, ctx := newAttachmentTaskSQLiteStore(t)
	queuedKey := "queued-update-key"
	var attachment *model.Attachment
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		var err error
		attachment, err = store.CreateAttachment(txCtx, "user1", "", model.Attachment{})
		if err != nil {
			return err
		}
		if err := registrystore.CreateAttachmentBlobDeleteTask(txCtx, store, queuedKey); err != nil {
			return err
		}
		_, err = store.UpdateAttachment(txCtx, "user1", attachment.ID, registrystore.AttachmentUpdate{StorageKey: &queuedKey})
		return err
	}))
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		tasks, err := store.ClaimReadyTasks(txCtx, 10)
		require.NoError(t, err)
		require.Empty(t, tasks)
		return nil
	}))

	claimedKey := "claimed-update-key"
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		if err := registrystore.CreateAttachmentBlobDeleteTask(txCtx, store, claimedKey); err != nil {
			return err
		}
		tasks, err := store.ClaimReadyTasks(txCtx, 1)
		if err != nil {
			return err
		}
		require.Len(t, tasks, 1)
		return store.FailTask(txCtx, tasks[0].ID, "provider unavailable", 0)
	}))
	err := store.InWriteTx(ctx, func(txCtx context.Context) error {
		_, err := store.UpdateAttachment(txCtx, "user1", attachment.ID, registrystore.AttachmentUpdate{StorageKey: &claimedKey})
		return err
	})
	var conflict *registrystore.ConflictError
	require.ErrorAs(t, err, &conflict)

	var stored *model.Attachment
	require.NoError(t, store.InReadTx(ctx, func(txCtx context.Context) error {
		var err error
		stored, err = store.GetAttachment(txCtx, "user1", "", attachment.ID)
		return err
	}))
	require.Equal(t, queuedKey, *stored.StorageKey)
}

func TestPrepareAttachmentBlobDeleteRetiresTaskForLiveReference(t *testing.T) {
	store, ctx := newAttachmentTaskSQLiteStore(t)
	storageKey := "shared-key"
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		if _, err := store.CreateAttachment(txCtx, "user1", "", model.Attachment{StorageKey: &storageKey}); err != nil {
			return err
		}
		return registrystore.CreateAttachmentBlobDeleteTask(txCtx, store, storageKey)
	}))
	var taskID uuid.UUID
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		tasks, err := store.ClaimReadyTasks(txCtx, 1)
		if err != nil {
			return err
		}
		require.Len(t, tasks, 1)
		taskID = tasks[0].ID
		return nil
	}))
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		referenced, err := store.PrepareAttachmentBlobDelete(txCtx, taskID, storageKey)
		require.NoError(t, err)
		require.True(t, referenced)
		return nil
	}))
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		tasks, err := store.ClaimReadyTasks(txCtx, 10)
		require.NoError(t, err)
		require.Empty(t, tasks)
		return nil
	}))
}

func TestCompletedAttachmentBlobDeletesDoNotAccumulateTasks(t *testing.T) {
	store, ctx := newAttachmentTaskSQLiteStore(t)
	for i := 0; i < 100; i++ {
		storageKey := fmt.Sprintf("deleted-key-%d", i)
		require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
			return registrystore.CreateAttachmentBlobDeleteTask(txCtx, store, storageKey)
		}))
		require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
			tasks, err := store.ClaimReadyTasks(txCtx, 1)
			if err != nil {
				return err
			}
			require.Len(t, tasks, 1)
			return store.CompleteAttachmentBlobDelete(txCtx, tasks[0].ID)
		}))
	}

	sqliteStore := store.(*SQLiteStore)
	require.NoError(t, store.InReadTx(ctx, func(txCtx context.Context) error {
		var count int64
		require.NoError(t, sqliteStore.dbFor(txCtx).Model(&model.Task{}).Count(&count).Error)
		require.Zero(t, count)
		return nil
	}))
}

func TestReadyTaskScanSkipsReferenceGuards(t *testing.T) {
	store, ctx := newAttachmentTaskSQLiteStore(t)
	sqliteStore := store.(*SQLiteStore)
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		for i := 0; i < 1000; i++ {
			if err := store.RegisterAttachmentBlobReference(txCtx, fmt.Sprintf("guard-%d", i)); err != nil {
				return err
			}
		}
		return store.CreateTask(txCtx, "generic-ready", map[string]any{"value": "ready"})
	}))
	// Simulate a version-2 datastore created before the ready-time index was
	// added. Startup migration must add it without a schema reset.
	require.NoError(t, sqliteStore.db.WithContext(ctx).Exec("DROP INDEX idx_tasks_ready_v2").Error)
	require.NoError(t, (&sqliteMigrator{}).Migrate(ctx))

	type explainRow struct {
		Detail string `gorm:"column:detail"`
	}
	var planRows []explainRow
	require.NoError(t, sqliteStore.db.WithContext(ctx).Raw(`
		EXPLAIN QUERY PLAN SELECT id
		FROM tasks
		WHERE (task_type <> ? OR COALESCE(json_extract(task_body, '$.state'), ?) IN (?, ?))
		  AND (retry_at, retry_at_ns) <= (?, ?)
		ORDER BY retry_at, retry_at_ns, created_at, created_at_ns
		LIMIT 1
	`, registrystore.AttachmentBlobDeleteTaskType,
		registrystore.AttachmentBlobDeleteStateQueued,
		registrystore.AttachmentBlobDeleteStateQueued,
		registrystore.AttachmentBlobDeleteStateClaimed,
		time.Now().UTC()).
		Scan(&planRows).Error)
	var plan strings.Builder
	for _, row := range planRows {
		plan.WriteString(row.Detail)
		plan.WriteByte('\n')
	}
	require.Contains(t, plan.String(), "idx_tasks_ready_v2")

	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		tasks, err := store.ClaimReadyTasks(txCtx, 1)
		require.NoError(t, err)
		require.Len(t, tasks, 1)
		require.Equal(t, "generic-ready", tasks[0].TaskType)
		return nil
	}))
}

func newAttachmentTaskSQLiteStore(t *testing.T) (registrystore.MemoryStore, context.Context) {
	t.Helper()
	cfg := &config.Config{
		DatastoreType:           "sqlite",
		DBURL:                   filepath.Join(t.TempDir(), "attachment-task.db"),
		DatastoreMigrateAtStart: true,
		EncryptionDBDisabled:    true,
	}
	ctx := config.WithContext(context.Background(), cfg)
	require.NoError(t, (&sqliteMigrator{}).Migrate(ctx))
	loader, err := registrystore.Select("sqlite")
	require.NoError(t, err)
	store, err := loader(ctx)
	require.NoError(t, err)
	return store, ctx
}
