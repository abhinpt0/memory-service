package entries

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chirino/memory-service/internal/model"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
	"github.com/chirino/memory-service/internal/security"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type retryingAppendStore struct {
	registrystore.MemoryStore
	attempt         int
	sourceID        uuid.UUID
	sourceEntryID   uuid.UUID
	groupID         uuid.UUID
	cloneIDs        [2]uuid.UUID
	appendedContent []json.RawMessage
}

func (s *retryingAppendStore) InWriteTx(ctx context.Context, fn func(context.Context) error) error {
	s.attempt = 0
	if err := fn(ctx); err != nil {
		return err
	}
	s.attempt = 1
	return fn(ctx)
}

func (s *retryingAppendStore) GetConversation(context.Context, string, string) (*registrystore.ConversationDetail, error) {
	return &registrystore.ConversationDetail{ConversationSummary: registrystore.ConversationSummary{
		ID: "conversation-1", ConversationGroupID: s.groupID, AccessLevel: model.AccessLevelOwner,
	}}, nil
}

func (s *retryingAppendStore) GetAttachment(_ context.Context, _ string, _ string, id uuid.UUID) (*model.Attachment, error) {
	if id != s.sourceID {
		return nil, &registrystore.NotFoundError{Resource: "attachment", ID: id.String()}
	}
	storageKey := "shared-key"
	return &model.Attachment{
		ID: s.sourceID, EntryID: &s.sourceEntryID, StorageKey: &storageKey,
		ContentType: "text/plain", UserID: "user-1", Status: "ready",
	}, nil
}

func (s *retryingAppendStore) GetEntryGroupID(context.Context, uuid.UUID) (uuid.UUID, error) {
	return s.groupID, nil
}

func (s *retryingAppendStore) CloneAttachment(context.Context, string, uuid.UUID) (*model.Attachment, error) {
	return &model.Attachment{ID: s.cloneIDs[s.attempt], ContentType: "text/plain"}, nil
}

func (s *retryingAppendStore) AppendEntries(_ context.Context, _ string, conversationID string, entries []registrystore.CreateEntryRequest, _ *string, _ *string, _ *int64) ([]model.Entry, error) {
	content := append(json.RawMessage(nil), entries[0].Content...)
	s.appendedContent = append(s.appendedContent, content)
	return []model.Entry{{ID: uuid.New(), ConversationID: conversationID, Channel: model.ChannelHistory, ContentType: entries[0].ContentType, Content: content}}, nil
}

func (s *retryingAppendStore) LinkAttachmentToEntry(_ context.Context, _ string, attachmentID, entryID uuid.UUID) (*model.Attachment, error) {
	return &model.Attachment{ID: attachmentID, EntryID: &entryID}, nil
}

func TestAppendEntryRetryClonesImmutableAttachmentRequestPerAttempt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sourceID := uuid.New()
	store := &retryingAppendStore{
		sourceID: sourceID, sourceEntryID: uuid.New(), groupID: uuid.New(),
		cloneIDs: [2]uuid.UUID{uuid.New(), uuid.New()},
	}
	body, err := json.Marshal(map[string]any{
		"contentType": "application/json",
		"content": []any{map[string]any{
			"type": "text", "text": "hello",
			"attachments": []any{map[string]any{"attachmentId": sourceID.String(), "contentType": "text/plain"}},
		}},
	})
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/conversations/conversation-1/entries", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "conversationId", Value: "conversation-1"}}
	c.Set(security.ContextKeyUserID, "user-1")

	appendEntry(c, store, nil)

	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	require.Len(t, store.appendedContent, 2)
	require.Contains(t, string(store.appendedContent[0]), store.cloneIDs[0].String())
	require.Contains(t, string(store.appendedContent[1]), store.cloneIDs[1].String())
	require.NotContains(t, string(store.appendedContent[1]), store.cloneIDs[0].String())
	require.Contains(t, recorder.Body.String(), store.cloneIDs[1].String())
}
