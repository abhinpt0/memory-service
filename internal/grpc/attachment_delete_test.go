package grpc

import (
	"context"
	"errors"
	"io"
	"net/url"
	"testing"
	"time"

	"github.com/chirino/memory-service/internal/config"
	pb "github.com/chirino/memory-service/internal/generated/pb/memory/v1"
	"github.com/chirino/memory-service/internal/model"
	registryattach "github.com/chirino/memory-service/internal/registry/attach"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
	"github.com/chirino/memory-service/internal/security"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gogrpc "google.golang.org/grpc"
)

type retryingGRPCAttachmentStore struct {
	registrystore.MemoryStore
	attachment      model.Attachment
	metadataDeletes int
	tasks           []map[string]any
	taskTypes       []string
	createErr       error
}

func (s *retryingGRPCAttachmentStore) InWriteTx(ctx context.Context, fn func(context.Context) error) error {
	if err := fn(ctx); err != nil {
		return err
	}
	return fn(ctx)
}

func (s *retryingGRPCAttachmentStore) GetAttachment(context.Context, string, string, uuid.UUID) (*model.Attachment, error) {
	attachment := s.attachment
	return &attachment, nil
}

func (s *retryingGRPCAttachmentStore) CreateAttachment(_ context.Context, _, _ string, attachment model.Attachment) (*model.Attachment, error) {
	if s.createErr != nil {
		return nil, s.createErr
	}
	return &attachment, nil
}

func (s *retryingGRPCAttachmentStore) DeleteAttachment(context.Context, string, string, uuid.UUID) error {
	s.metadataDeletes++
	if s.attachment.StorageKey != nil {
		return s.QueueAttachmentBlobDelete(context.Background(), *s.attachment.StorageKey)
	}
	return nil
}

func (s *retryingGRPCAttachmentStore) CreateTask(_ context.Context, taskType string, body map[string]any) error {
	s.taskTypes = append(s.taskTypes, taskType)
	s.tasks = append(s.tasks, body)
	return nil
}

func (s *retryingGRPCAttachmentStore) QueueAttachmentBlobDelete(_ context.Context, storageKey string) error {
	s.taskTypes = append(s.taskTypes, registrystore.AttachmentBlobDeleteTaskType)
	s.tasks = append(s.tasks, map[string]any{registrystore.AttachmentBlobDeleteStorageKeyField: storageKey})
	return nil
}

type countingGRPCAttachmentProvider struct {
	deleteCalls int
}

func (s *countingGRPCAttachmentProvider) Store(context.Context, io.Reader, int64, string) (*registryattach.FileStoreResult, error) {
	return nil, errors.New("not implemented")
}

func (s *countingGRPCAttachmentProvider) Retrieve(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}

func (s *countingGRPCAttachmentProvider) Delete(context.Context, string) error {
	s.deleteCalls++
	return nil
}

func (s *countingGRPCAttachmentProvider) GetSignedURL(context.Context, string, time.Duration, *registryattach.SignedURLOptions) (*url.URL, error) {
	return nil, errors.New("not implemented")
}

func TestGRPCDeleteAttachmentOnlyEnqueuesBlobDeleteDuringTransactionRetries(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Mode = config.ModeTesting
	cfg.LocalUserID = "alice"
	cfg.UnixSocketAuth = "local"
	resolver, err := security.NewTokenResolver(&cfg)
	require.NoError(t, err)

	attachmentID := uuid.New()
	storageKey := "stored-key"
	store := &retryingGRPCAttachmentStore{attachment: model.Attachment{ID: attachmentID, StorageKey: &storageKey}}
	provider := &countingGRPCAttachmentProvider{}
	server := &AttachmentsServer{Store: store, AttachStore: provider, Config: &cfg}
	request := &pb.DeleteAttachmentRequest{Id: attachmentID.String()}
	interceptor := security.GRPCUnaryInterceptorWithRateLimiter(resolver, nil)
	_, err = interceptor(context.Background(), request, &gogrpc.UnaryServerInfo{}, func(ctx context.Context, raw any) (any, error) {
		return server.DeleteAttachment(ctx, raw.(*pb.DeleteAttachmentRequest))
	})
	require.NoError(t, err)
	require.Equal(t, 2, store.metadataDeletes)
	require.Len(t, store.tasks, 2)
	require.Equal(t, []string{registrystore.AttachmentBlobDeleteTaskType, registrystore.AttachmentBlobDeleteTaskType}, store.taskTypes)
	for _, body := range store.tasks {
		require.Equal(t, storageKey, body[registrystore.AttachmentBlobDeleteStorageKeyField])
	}
	require.Zero(t, provider.deleteCalls)
}
