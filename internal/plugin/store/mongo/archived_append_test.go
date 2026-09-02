//go:build !nomongo

package mongo

import (
	"context"
	"testing"

	"github.com/chirino/memory-service/internal/config"
	"github.com/chirino/memory-service/internal/model"
	registrymigrate "github.com/chirino/memory-service/internal/registry/migrate"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
	"github.com/chirino/memory-service/internal/testutil/testmongo"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestAppendEntriesBeforeUnarchiveRequiresExplicitOperation(t *testing.T) {
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

	conversation, err := store.CreateConversation(ctx, "user1", "client1", "Archived", nil, nil, nil, nil)
	require.NoError(t, err)
	require.NoError(t, store.ArchiveConversation(ctx, "user1", conversation.ID))
	seq := uint32(7)
	entry := registrystore.CreateEntryRequest{
		Seq:         &seq,
		Channel:     "history",
		ContentType: "history",
		Content:     []byte(`[{"type":"message","role":"USER","content":"first"}]`),
	}

	_, err = store.AppendEntries(ctx, "user1", conversation.ID, []registrystore.CreateEntryRequest{entry}, nil, nil, nil)
	var notFound *registrystore.NotFoundError
	require.ErrorAs(t, err, &notFound)

	created, err := store.AppendEntriesBeforeUnarchive(ctx, "user1", conversation.ID, []registrystore.CreateEntryRequest{entry}, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, created, 1)
	stillArchived, err := store.GetConversation(ctx, "user1", conversation.ID)
	require.NoError(t, err)
	require.NotNil(t, stillArchived.ArchivedAt)

	entry.Content = []byte(`[{"type":"message","role":"USER","content":"changed"}]`)
	_, err = store.AppendEntriesBeforeUnarchive(ctx, "user1", conversation.ID, []registrystore.CreateEntryRequest{entry}, nil, nil, nil)
	require.True(t, registrystore.IsDuplicateSequenceConflict(err))
	stillArchived, err = store.GetConversation(ctx, "user1", conversation.ID)
	require.NoError(t, err)
	require.NotNil(t, stillArchived.ArchivedAt)

	firstUnarchive, err := store.UnarchiveConversationIfNeeded(ctx, "user1", conversation.ID)
	require.NoError(t, err)
	require.True(t, firstUnarchive.Changed)
	require.Equal(t, conversation.ConversationGroupID, firstUnarchive.ConversationGroupID)
	secondUnarchive, err := store.UnarchiveConversationIfNeeded(ctx, "user1", conversation.ID)
	require.NoError(t, err)
	require.False(t, secondUnarchive.Changed)
	require.Equal(t, conversation.ConversationGroupID, secondUnarchive.ConversationGroupID)
	firstArchive, err := store.ArchiveConversationIfNeeded(ctx, "user1", conversation.ID)
	require.NoError(t, err)
	require.True(t, firstArchive.Changed)
	require.Equal(t, conversation.ConversationGroupID, firstArchive.ConversationGroupID)
	secondArchive, err := store.ArchiveConversationIfNeeded(ctx, "user1", conversation.ID)
	require.NoError(t, err)
	require.False(t, secondArchive.Changed)
	require.Equal(t, conversation.ConversationGroupID, secondArchive.ConversationGroupID)

	mongoStore := store.(*MongoStore)
	var authoritative groupDoc
	require.NoError(t, mongoStore.groups().FindOne(ctx, bson.M{"_id": conversation.ConversationGroupID.String()}).Decode(&authoritative))
	require.NotNil(t, authoritative.ArchivedAt)
	_, err = mongoStore.conversations().UpdateMany(ctx,
		bson.M{"conversation_group_id": conversation.ConversationGroupID.String()},
		bson.M{"$unset": bson.M{"archived_at": ""}},
	)
	require.NoError(t, err)
	repairedArchive, err := store.ArchiveConversationIfNeeded(ctx, "user1", conversation.ID)
	require.NoError(t, err)
	require.False(t, repairedArchive.Changed)
	require.Equal(t, conversation.ConversationGroupID, repairedArchive.ConversationGroupID)
	repaired, err := store.GetConversation(ctx, "user1", conversation.ID)
	require.NoError(t, err)
	require.NotNil(t, repaired.ArchivedAt)
	require.Equal(t, authoritative.ArchivedAt.UTC(), repaired.ArchivedAt.UTC())
}

func TestRepairStoredEntryAttachmentLinks(t *testing.T) {
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
	conversation, err := store.CreateConversation(ctx, "user1", "client1", "Attachment repair", nil, nil, nil, nil)
	require.NoError(t, err)
	storageKey := "repair-key"
	attachment, err := store.CreateAttachment(ctx, "user1", "", model.Attachment{StorageKey: &storageKey, ContentType: "text/plain", Status: "ready"})
	require.NoError(t, err)
	seq := uint32(9)
	entries, err := store.AppendEntries(ctx, "user1", conversation.ID, []registrystore.CreateEntryRequest{{
		Seq: &seq, Channel: "history", ContentType: "history",
		Content: []byte(`[{"role":"USER","attachments":[{"attachmentId":"` + attachment.ID.String() + `"}]}]`),
	}}, nil, nil, nil)
	require.NoError(t, err)
	require.Nil(t, attachment.EntryID)

	require.NoError(t, registrystore.RepairStoredEntryAttachmentLinks(ctx, store, "user1", entries[0]))
	linked, err := store.GetAttachment(ctx, "user1", "", attachment.ID)
	require.NoError(t, err)
	require.NotNil(t, linked.EntryID)
	require.Equal(t, entries[0].ID, *linked.EntryID)
	require.NoError(t, registrystore.RepairStoredEntryAttachmentLinks(ctx, store, "user1", entries[0]))
	require.Error(t, store.DeleteAttachment(ctx, "user1", "", attachment.ID))

	otherEntryID := uuid.New()
	otherAttachment, err := store.CreateAttachment(ctx, "user1", "", model.Attachment{StorageKey: &storageKey, ContentType: "text/plain", Status: "ready"})
	require.NoError(t, err)
	_, err = store.LinkAttachmentToEntry(ctx, "user1", otherAttachment.ID, otherEntryID)
	require.NoError(t, err)
	_, err = store.LinkAttachmentToEntry(ctx, "user1", otherAttachment.ID, entries[0].ID)
	require.Error(t, err)
	preserved, err := store.GetAttachment(ctx, "user1", "", otherAttachment.ID)
	require.NoError(t, err)
	require.Equal(t, otherEntryID, *preserved.EntryID)
}
