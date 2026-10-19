package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chirino/memory-service/internal/model"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type retryingAdminAttachmentStore struct {
	registrystore.MemoryStore
	attachment      registrystore.AdminAttachment
	metadataDeletes int
	taskTypes       []string
	taskBodies      []map[string]any
}

func (s *retryingAdminAttachmentStore) InWriteTx(ctx context.Context, fn func(context.Context) error) error {
	if err := fn(ctx); err != nil {
		return err
	}
	return fn(ctx)
}

func (s *retryingAdminAttachmentStore) AdminGetAttachment(context.Context, uuid.UUID) (*registrystore.AdminAttachment, error) {
	attachment := s.attachment
	return &attachment, nil
}

func (s *retryingAdminAttachmentStore) AdminDeleteAttachment(context.Context, uuid.UUID) error {
	s.metadataDeletes++
	if s.attachment.StorageKey != nil {
		return s.QueueAttachmentBlobDelete(context.Background(), *s.attachment.StorageKey)
	}
	return nil
}

func (s *retryingAdminAttachmentStore) CreateTask(_ context.Context, taskType string, body map[string]any) error {
	s.taskTypes = append(s.taskTypes, taskType)
	s.taskBodies = append(s.taskBodies, body)
	return nil
}

func (s *retryingAdminAttachmentStore) QueueAttachmentBlobDelete(_ context.Context, storageKey string) error {
	s.taskTypes = append(s.taskTypes, registrystore.AttachmentBlobDeleteTaskType)
	s.taskBodies = append(s.taskBodies, map[string]any{registrystore.AttachmentBlobDeleteStorageKeyField: storageKey})
	return nil
}

func TestAdminDeleteAttachmentEnqueuesBlobDeleteEvenWithoutConfiguredProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	attachmentID := uuid.New()
	storageKey := "stored-key"
	store := &retryingAdminAttachmentStore{attachment: registrystore.AdminAttachment{
		Attachment: model.Attachment{ID: attachmentID, StorageKey: &storageKey},
		RefCount:   2,
	}}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v1/admin/attachments/"+attachmentID.String(), nil)
	c.Params = gin.Params{{Key: "id", Value: attachmentID.String()}}

	adminDeleteAttachment(c, store, nil)

	require.Equal(t, http.StatusNoContent, c.Writer.Status())
	require.Equal(t, 2, store.metadataDeletes)
	require.Equal(t, []string{registrystore.AttachmentBlobDeleteTaskType, registrystore.AttachmentBlobDeleteTaskType}, store.taskTypes)
	for _, body := range store.taskBodies {
		require.Equal(t, storageKey, body[registrystore.AttachmentBlobDeleteStorageKeyField])
	}
}
