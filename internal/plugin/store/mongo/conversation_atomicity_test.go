//go:build !nomongo

package mongo

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/chirino/memory-service/internal/config"
	"github.com/chirino/memory-service/internal/model"
	registrymigrate "github.com/chirino/memory-service/internal/registry/migrate"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
	"github.com/chirino/memory-service/internal/testutil/testbarrier"
	"github.com/chirino/memory-service/internal/testutil/testmongo"
	"github.com/chirino/memory-service/internal/txscope"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func setupTestMongoStore(t *testing.T, outboxEnabled bool) (registrystore.MemoryStore, *MongoStore, context.Context) {
	t.Helper()
	dbURL := testmongo.StartMongo(t)
	cfg := config.DefaultConfig()
	cfg.DBURL = dbURL
	cfg.DatastoreType = "mongo"
	cfg.OutboxEnabled = outboxEnabled
	ctx := config.WithContext(context.Background(), &cfg)

	err := registrymigrate.RunAll(ctx)
	require.NoError(t, err)

	loader, err := registrystore.Select("mongo")
	require.NoError(t, err)
	store, err := loader(ctx)
	require.NoError(t, err)
	mongoStore, ok := store.(*MongoStore)
	require.True(t, ok)
	t.Cleanup(func() {
		require.NoError(t, mongoStore.client.Disconnect(context.Background()))
	})

	return store, mongoStore, ctx
}

func TestMongoConversationAtomicityFaultBeforeConversationInsert_Root(t *testing.T) {
	store, mongoStore, ctx := setupTestMongoStore(t, false)

	initialGroups, err := mongoStore.groups().CountDocuments(ctx, bson.M{})
	require.NoError(t, err)

	convID := "root-fault-conv-" + uuid.NewString()
	faultErr := errors.New("injected fault before conversation insert")

	mongoStore.beforeConversationInsert = func() error {
		return faultErr
	}

	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		_, err := mongoStore.CreateConversationWithID(txCtx, "user1", "client1", convID, "Test Root", nil, nil, nil, nil)
		return err
	})
	require.ErrorIs(t, err, faultErr)

	// Verify rollback: no group, no ancestry, no conversation in DB
	convCount, err := mongoStore.conversations().CountDocuments(ctx, bson.M{"_id": convID})
	require.NoError(t, err)
	assert.Equal(t, int64(0), convCount, "conversation doc must be rolled back")

	ancestryCount, err := mongoStore.conversationAncestry().CountDocuments(ctx, bson.M{"_id": convID})
	require.NoError(t, err)
	assert.Equal(t, int64(0), ancestryCount, "ancestry doc must be rolled back")

	groupCount, err := mongoStore.groups().CountDocuments(ctx, bson.M{})
	require.NoError(t, err)
	assert.Equal(t, initialGroups, groupCount, "conversation group must be rolled back")

	// Retry without fault
	mongoStore.beforeConversationInsert = nil
	var result *registrystore.CreateConversationResult
	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		var createErr error
		result, createErr = mongoStore.CreateConversationWithID(txCtx, "user1", "client1", convID, "Test Root", nil, nil, nil, nil)
		return createErr
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, convID, result.Conversation.ID)

	convCount, err = mongoStore.conversations().CountDocuments(ctx, bson.M{"_id": convID})
	require.NoError(t, err)
	assert.Equal(t, int64(1), convCount)

	ancestryCount, err = mongoStore.conversationAncestry().CountDocuments(ctx, bson.M{"_id": convID})
	require.NoError(t, err)
	assert.Equal(t, int64(1), ancestryCount)

	groupCount, err = mongoStore.groups().CountDocuments(ctx, bson.M{})
	require.NoError(t, err)
	assert.Equal(t, initialGroups+1, groupCount, "conversation group must be created on retry")
}

func TestMongoConversationAtomicityFaultBeforeConversationInsert_StartedBy(t *testing.T) {
	store, mongoStore, ctx := setupTestMongoStore(t, false)

	// Create parent conversation first
	parentID := "parent-conv-" + uuid.NewString()
	err := store.InWriteTx(ctx, func(txCtx context.Context) error {
		_, err := mongoStore.CreateConversationWithID(txCtx, "user1", "client1", parentID, "Parent", nil, nil, nil, nil)
		return err
	})
	require.NoError(t, err)

	initialGroups, err := mongoStore.groups().CountDocuments(ctx, bson.M{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), initialGroups)

	startedByID := "started-by-fault-conv-" + uuid.NewString()
	faultErr := errors.New("injected fault before conversation insert")

	mongoStore.beforeConversationInsert = func() error {
		return faultErr
	}

	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		_, err := mongoStore.createConversation(txCtx, "user1", "client1", nil, startedByID, "Started By", nil, nil, nil, &parentID, nil)
		return err
	})
	require.ErrorIs(t, err, faultErr)

	// Verify rollback
	convCount, err := mongoStore.conversations().CountDocuments(ctx, bson.M{"_id": startedByID})
	require.NoError(t, err)
	assert.Equal(t, int64(0), convCount)

	ancestryCount, err := mongoStore.conversationAncestry().CountDocuments(ctx, bson.M{"_id": startedByID})
	require.NoError(t, err)
	assert.Equal(t, int64(0), ancestryCount)

	groupCount, err := mongoStore.groups().CountDocuments(ctx, bson.M{})
	require.NoError(t, err)
	assert.Equal(t, initialGroups, groupCount, "started-by conversation group must be rolled back")

	// Retry without fault
	mongoStore.beforeConversationInsert = nil
	var result *registrystore.CreateConversationResult
	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		var createErr error
		result, createErr = mongoStore.createConversation(txCtx, "user1", "client1", nil, startedByID, "Started By", nil, nil, nil, &parentID, nil)
		return createErr
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, startedByID, result.Conversation.ID)

	groupCount, err = mongoStore.groups().CountDocuments(ctx, bson.M{})
	require.NoError(t, err)
	assert.Equal(t, initialGroups+1, groupCount, "started-by conversation group must be created on retry")
}

func TestMongoConversationAtomicityFaultBeforeMembershipInsert(t *testing.T) {
	store, mongoStore, ctx := setupTestMongoStore(t, false)

	initialGroups, err := mongoStore.groups().CountDocuments(ctx, bson.M{})
	require.NoError(t, err)

	convID := "membership-fault-conv-" + uuid.NewString()
	faultErr := errors.New("injected fault before membership insert")

	mongoStore.beforeMembershipInsert = func() error {
		return faultErr
	}

	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		_, err := mongoStore.CreateConversationWithID(txCtx, "user1", "client1", convID, "Test Membership Fault", nil, nil, nil, nil)
		return err
	})
	require.ErrorIs(t, err, faultErr)

	// Verify full rollback: conversation doc rolled back so no headless conversation exists
	convCount, err := mongoStore.conversations().CountDocuments(ctx, bson.M{"_id": convID})
	require.NoError(t, err)
	assert.Equal(t, int64(0), convCount)

	ancestryCount, err := mongoStore.conversationAncestry().CountDocuments(ctx, bson.M{"_id": convID})
	require.NoError(t, err)
	assert.Equal(t, int64(0), ancestryCount)

	groupCount, err := mongoStore.groups().CountDocuments(ctx, bson.M{})
	require.NoError(t, err)
	assert.Equal(t, initialGroups, groupCount, "conversation group must be rolled back")

	membershipCount, err := mongoStore.memberships().CountDocuments(ctx, bson.M{})
	require.NoError(t, err)
	assert.Equal(t, int64(0), membershipCount, "membership doc must not be created")

	// Retry without fault
	mongoStore.beforeMembershipInsert = nil
	var result *registrystore.CreateConversationResult
	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		var createErr error
		result, createErr = mongoStore.CreateConversationWithID(txCtx, "user1", "client1", convID, "Test Membership Fault", nil, nil, nil, nil)
		return createErr
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, convID, result.Conversation.ID)

	// Verify membership exists
	detail, err := store.GetConversation(ctx, "user1", convID)
	require.NoError(t, err)
	assert.Equal(t, convID, detail.ID)
}

func TestMongoConversationAtomicityFaultBeforeMembershipInsert_StartedBy(t *testing.T) {
	store, mongoStore, ctx := setupTestMongoStore(t, false)

	parentID := "membership-parent-" + uuid.NewString()
	err := store.InWriteTx(ctx, func(txCtx context.Context) error {
		_, createErr := mongoStore.CreateConversationWithID(txCtx, "user1", "client1", parentID, "Parent", nil, nil, nil, nil)
		return createErr
	})
	require.NoError(t, err)

	initialGroups, err := mongoStore.groups().CountDocuments(ctx, bson.M{})
	require.NoError(t, err)
	childID := "membership-child-" + uuid.NewString()
	faultErr := errors.New("injected started-by fault before membership insert")
	mongoStore.beforeMembershipInsert = func() error { return faultErr }

	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		_, createErr := mongoStore.createConversation(txCtx, "user1", "client1", nil, childID, "Child", nil, nil, nil, &parentID, nil)
		return createErr
	})
	require.ErrorIs(t, err, faultErr)

	convCount, err := mongoStore.conversations().CountDocuments(ctx, bson.M{"_id": childID})
	require.NoError(t, err)
	assert.Equal(t, int64(0), convCount)
	ancestryCount, err := mongoStore.conversationAncestry().CountDocuments(ctx, bson.M{"_id": childID})
	require.NoError(t, err)
	assert.Equal(t, int64(0), ancestryCount)
	groupCount, err := mongoStore.groups().CountDocuments(ctx, bson.M{})
	require.NoError(t, err)
	assert.Equal(t, initialGroups, groupCount)

	mongoStore.beforeMembershipInsert = nil
	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		_, createErr := mongoStore.createConversation(txCtx, "user1", "client1", nil, childID, "Child", nil, nil, nil, &parentID, nil)
		return createErr
	})
	require.NoError(t, err)

	detail, err := store.GetConversation(ctx, "user1", childID)
	require.NoError(t, err)
	assert.Equal(t, childID, detail.ID)
}

func TestMongoConversationConcurrentLiveCreator(t *testing.T) {
	store, mongoStore, ctx := setupTestMongoStore(t, false)

	initialGroups, err := mongoStore.groups().CountDocuments(ctx, bson.M{})
	require.NoError(t, err)

	convID := "concurrent-live-conv-" + uuid.NewString()

	barrier := testbarrier.New(2)
	mongoStore.afterConversationMissing = func() error {
		barrier.Wait()
		return nil
	}
	t.Cleanup(func() { mongoStore.afterConversationMissing = nil })

	var wg sync.WaitGroup
	var res1, res2 *registrystore.CreateConversationResult
	var err1, err2 error

	wg.Add(2)
	go func() {
		defer wg.Done()
		err1 = store.InWriteTx(ctx, func(txCtx context.Context) error {
			var err error
			res1, err = mongoStore.CreateConversationWithID(txCtx, "user1", "client1", convID, "Concurrent 1", nil, nil, nil, nil)
			return err
		})
	}()

	go func() {
		defer wg.Done()
		err2 = store.InWriteTx(ctx, func(txCtx context.Context) error {
			var err error
			res2, err = mongoStore.CreateConversationWithID(txCtx, "user1", "client1", convID, "Concurrent 1", nil, nil, nil, nil)
			return err
		})
	}()

	barrier.WaitForParties(t, 2)
	wg.Wait()

	require.NoError(t, err1)
	require.NoError(t, err2)

	require.NotNil(t, res1)
	require.NotNil(t, res2)

	// Exactly one must be new (not ExactRetry) and one must be ExactRetry (or both succeed cleanly)
	assert.Equal(t, convID, res1.Conversation.ID)
	assert.Equal(t, convID, res2.Conversation.ID)
	assert.True(t, res1.ExactRetry || res2.ExactRetry, "one concurrent creator should be exact retry")

	convCount, err := mongoStore.conversations().CountDocuments(ctx, bson.M{"_id": convID})
	require.NoError(t, err)
	assert.Equal(t, int64(1), convCount)

	ancestryCount, err := mongoStore.conversationAncestry().CountDocuments(ctx, bson.M{"_id": convID})
	require.NoError(t, err)
	assert.Equal(t, int64(1), ancestryCount)

	// Both creators passed the missing-row precheck. The losing transaction is
	// retried and its first-attempt group insert is rolled back.
	groupCount, err := mongoStore.groups().CountDocuments(ctx, bson.M{})
	require.NoError(t, err)
	assert.Equal(t, initialGroups+1, groupCount, "losing concurrent creator's group must be rolled back by transaction retry")
}

func TestMongoWriteTransactionRunsOnlyCommittedAfterCommitEffects(t *testing.T) {
	store, _, ctx := setupTestMongoStore(t, false)

	attempt := 0
	var committedAttempts []int
	err := store.InWriteTx(ctx, func(txCtx context.Context) error {
		attempt++
		currentAttempt := attempt
		txscope.AfterCommit(txCtx, func(context.Context) {
			committedAttempts = append(committedAttempts, currentAttempt)
		})
		if attempt == 1 {
			return mongo.CommandError{
				Message: "retry transaction",
				Labels:  []string{"TransientTransactionError"},
			}
		}
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, 2, attempt)
	assert.Equal(t, []int{2}, committedAttempts)
}

func TestMongoAfterCommitActionsDetachCancellationWithDeadline(t *testing.T) {
	type contextKey struct{}
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "preserved"))
	actionCtx, scope := txscope.WithAfterCommitScope(parent)

	var actionErr error
	var value any
	var hasDeadline bool
	txscope.AfterCommit(actionCtx, func(ctx context.Context) {
		actionErr = ctx.Err()
		value = ctx.Value(contextKey{})
		_, hasDeadline = ctx.Deadline()
	})
	cancel()

	runMongoAfterCommitActions(parent, scope)

	require.NoError(t, actionErr)
	require.Equal(t, "preserved", value)
	require.True(t, hasDeadline)
}

func TestMongoAttachmentBlobDeleteCoordination(t *testing.T) {
	store, _, ctx := setupTestMongoStore(t, false)
	queuedKey := "queued-key"
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		if err := registrystore.CreateAttachmentBlobDeleteTask(txCtx, store, queuedKey); err != nil {
			return err
		}
		_, err := store.CreateAttachment(txCtx, "user1", "", model.Attachment{StorageKey: &queuedKey})
		return err
	}))
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		tasks, err := store.ClaimReadyTasks(txCtx, 10)
		require.NoError(t, err)
		require.Empty(t, tasks)
		return nil
	}))
	queuedUpdateKey := "queued-update-key"
	var updateAttachment *model.Attachment
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		var err error
		updateAttachment, err = store.CreateAttachment(txCtx, "user1", "", model.Attachment{})
		if err != nil {
			return err
		}
		if err := registrystore.CreateAttachmentBlobDeleteTask(txCtx, store, queuedUpdateKey); err != nil {
			return err
		}
		_, err = store.UpdateAttachment(txCtx, "user1", updateAttachment.ID, registrystore.AttachmentUpdate{StorageKey: &queuedUpdateKey})
		return err
	}))
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		tasks, err := store.ClaimReadyTasks(txCtx, 10)
		require.NoError(t, err)
		require.Empty(t, tasks)
		return nil
	}))

	claimedKey := "claimed-key"
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		return registrystore.CreateAttachmentBlobDeleteTask(txCtx, store, claimedKey)
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
		return store.FailTask(txCtx, taskID, "provider unavailable", time.Hour)
	}))
	err := store.InWriteTx(ctx, func(txCtx context.Context) error {
		_, err := store.CreateAttachment(txCtx, "user1", "", model.Attachment{StorageKey: &claimedKey})
		return err
	})
	var conflict *registrystore.ConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, registrystore.AttachmentBlobDeleteConflictCode, conflict.Code)
	err = store.InWriteTx(ctx, func(txCtx context.Context) error {
		_, err := store.UpdateAttachment(txCtx, "user1", updateAttachment.ID, registrystore.AttachmentUpdate{StorageKey: &claimedKey})
		return err
	})
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, registrystore.AttachmentBlobDeleteConflictCode, conflict.Code)

	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		return store.CompleteAttachmentBlobDelete(txCtx, taskID)
	}))
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		_, err := store.CreateAttachment(txCtx, "user1", "", model.Attachment{StorageKey: &claimedKey})
		return err
	}))
}

func TestMongoAttachmentBlobGuardSerializesAbsentRow(t *testing.T) {
	for _, mode := range []string{"create", "update"} {
		t.Run(mode, func(t *testing.T) {
			store, _, ctx := setupTestMongoStore(t, false)
			storageKey := "absent-guard-" + mode
			var target *model.Attachment
			if mode == "update" {
				require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
					var err error
					target, err = store.CreateAttachment(txCtx, "user1", "", model.Attachment{})
					return err
				}))
			}

			guardWritten := make(chan struct{})
			releaseReference := make(chan struct{})
			referenceDone := make(chan error, 1)
			var guardOnce sync.Once
			go func() {
				referenceDone <- store.InWriteTx(ctx, func(txCtx context.Context) error {
					if err := store.RegisterAttachmentBlobReference(txCtx, storageKey); err != nil {
						return err
					}
					guardOnce.Do(func() { close(guardWritten) })
					<-releaseReference
					if mode == "create" {
						_, err := store.CreateAttachment(txCtx, "user1", "", model.Attachment{StorageKey: &storageKey})
						return err
					}
					_, err := store.UpdateAttachment(txCtx, "user1", target.ID, registrystore.AttachmentUpdate{StorageKey: &storageKey})
					return err
				})
			}()
			<-guardWritten

			cleanupStarted := make(chan struct{})
			cleanupDone := make(chan error, 1)
			var cleanupOnce sync.Once
			go func() {
				cleanupDone <- store.InWriteTx(ctx, func(txCtx context.Context) error {
					cleanupOnce.Do(func() { close(cleanupStarted) })
					return store.QueueAttachmentBlobDelete(txCtx, storageKey)
				})
			}()
			<-cleanupStarted
			close(releaseReference)
			require.NoError(t, <-referenceDone)
			require.NoError(t, <-cleanupDone)

			var task model.Task
			require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
				tasks, err := store.ClaimReadyTasks(txCtx, 1)
				if err != nil {
					return err
				}
				require.Len(t, tasks, 1)
				task = tasks[0]
				return nil
			}))
			require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
				referenced, err := store.PrepareAttachmentBlobDelete(txCtx, task.ID, storageKey)
				require.NoError(t, err)
				require.True(t, referenced, "provider deletion must be suppressed for the concurrently committed reference")
				return nil
			}))
		})
	}
}

func TestMongoAttachmentPrepareRetriesConcurrentFinalDelete(t *testing.T) {
	store, mongoStore, ctx := setupTestMongoStore(t, false)
	storageKey := "prepare-final-delete"
	var first, surviving *model.Attachment
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		var err error
		first, err = store.CreateAttachment(txCtx, "user1", "", model.Attachment{StorageKey: &storageKey})
		if err != nil {
			return err
		}
		surviving, err = store.CreateAttachment(txCtx, "user1", "", model.Attachment{StorageKey: &storageKey})
		return err
	}))
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		return store.AdminDeleteAttachment(txCtx, first.ID)
	}))

	var task model.Task
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		tasks, err := store.ClaimReadyTasks(txCtx, 1)
		if err != nil {
			return err
		}
		require.Len(t, tasks, 1)
		task = tasks[0]
		return nil
	}))

	counted := make(chan struct{})
	resumePrepare := make(chan struct{})
	var once sync.Once
	mongoStore.afterAttachmentDeleteCount = func() error {
		once.Do(func() {
			close(counted)
			<-resumePrepare
		})
		return nil
	}
	t.Cleanup(func() { mongoStore.afterAttachmentDeleteCount = nil })

	prepareResult := make(chan bool, 1)
	prepareDone := make(chan error, 1)
	go func() {
		var referenced bool
		err := store.InWriteTx(ctx, func(txCtx context.Context) error {
			var err error
			referenced, err = store.PrepareAttachmentBlobDelete(txCtx, task.ID, storageKey)
			return err
		})
		prepareResult <- referenced
		prepareDone <- err
	}()
	<-counted
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		return store.AdminDeleteAttachment(txCtx, surviving.ID)
	}))
	close(resumePrepare)
	require.NoError(t, <-prepareDone)
	require.False(t, <-prepareResult, "retried prepare must observe that the final reference was deleted")
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		return store.CompleteAttachmentBlobDelete(txCtx, task.ID)
	}))
}

func TestMongoCompletedAttachmentBlobDeletesDoNotAccumulateTasks(t *testing.T) {
	store, mongoStore, ctx := setupTestMongoStore(t, false)
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		for i := 0; i < 100; i++ {
			if err := store.QueueAttachmentBlobDelete(txCtx, fmt.Sprintf("completed-key-%d", i)); err != nil {
				return err
			}
		}
		return nil
	}))
	var tasks []model.Task
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		var err error
		tasks, err = store.ClaimReadyTasks(txCtx, 100)
		return err
	}))
	require.Len(t, tasks, 100)
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		for _, task := range tasks {
			if err := store.CompleteAttachmentBlobDelete(txCtx, task.ID); err != nil {
				return err
			}
		}
		return nil
	}))
	count, err := mongoStore.db.Collection("tasks").CountDocuments(ctx, bson.M{"task_type": registrystore.AttachmentBlobDeleteTaskType})
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestMongoCloneRejectsSourceDeletedBeforeLegacyGuardAcquisition(t *testing.T) {
	store, mongoStore, ctx := setupTestMongoStore(t, false)
	storageKey := "legacy-unguarded-key"
	sourceID := uuid.New()
	_, err := mongoStore.attachments().InsertOne(ctx, attachmentDoc{
		ID: sourceID.String(), StorageKey: &storageKey, UserID: "user1", Status: "ready", CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	sourceRead := make(chan struct{})
	resumeClone := make(chan struct{})
	var once sync.Once
	mongoStore.afterAttachmentCloneRead = func() error {
		once.Do(func() { close(sourceRead) })
		<-resumeClone
		return nil
	}
	t.Cleanup(func() { mongoStore.afterAttachmentCloneRead = nil })

	cloneDone := make(chan error, 1)
	go func() {
		cloneDone <- store.InWriteTx(ctx, func(txCtx context.Context) error {
			_, err := store.CloneAttachment(txCtx, "user1", sourceID)
			return err
		})
	}()
	<-sourceRead

	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		return store.AdminDeleteAttachment(txCtx, sourceID)
	}))
	var cleanup model.Task
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		tasks, err := store.ClaimReadyTasks(txCtx, 1)
		if err != nil {
			return err
		}
		require.Len(t, tasks, 1)
		cleanup = tasks[0]
		referenced, err := store.PrepareAttachmentBlobDelete(txCtx, cleanup.ID, storageKey)
		require.NoError(t, err)
		require.False(t, referenced)
		return nil
	}))
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		return store.CompleteAttachmentBlobDelete(txCtx, cleanup.ID)
	}))

	close(resumeClone)
	err = <-cloneDone
	var notFound *registrystore.NotFoundError
	require.ErrorAs(t, err, &notFound)
	count, err := mongoStore.attachments().CountDocuments(ctx, bson.M{"storage_key": storageKey})
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestMongoStandaloneRejectedWithoutOutbox(t *testing.T) {
	dbURL := testmongo.StartStandaloneMongo(t)
	cfg := config.DefaultConfig()
	cfg.DBURL = dbURL
	cfg.OutboxEnabled = false
	ctx := config.WithContext(context.Background(), &cfg)

	loader, err := registrystore.Select("mongo")
	require.NoError(t, err)
	_, err = loader(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MongoDB requires a replica set or mongos")
}
