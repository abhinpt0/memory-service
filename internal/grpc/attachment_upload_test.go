package grpc

import (
	"context"
	"io"
	"net/url"
	"testing"
	"time"

	"github.com/chirino/memory-service/internal/config"
	pb "github.com/chirino/memory-service/internal/generated/pb/memory/v1"
	registryattach "github.com/chirino/memory-service/internal/registry/attach"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
	"github.com/chirino/memory-service/internal/security"
	"github.com/stretchr/testify/require"
	gogrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type recordingUploadAttachmentProvider struct {
	storeCalls   int
	consumeInput bool
}

func (s *recordingUploadAttachmentProvider) Store(_ context.Context, reader io.Reader, _ int64, _ string) (*registryattach.FileStoreResult, error) {
	s.storeCalls++
	if s.consumeInput {
		if _, err := io.Copy(io.Discard, reader); err != nil {
			return nil, err
		}
	}
	return &registryattach.FileStoreResult{StorageKey: "stored-key"}, nil
}

func (*recordingUploadAttachmentProvider) Retrieve(context.Context, string) (io.ReadCloser, error) {
	return nil, nil
}

func (*recordingUploadAttachmentProvider) Delete(context.Context, string) error { return nil }

func (*recordingUploadAttachmentProvider) GetSignedURL(context.Context, string, time.Duration, *registryattach.SignedURLOptions) (*url.URL, error) {
	return nil, nil
}

type uploadAttachmentTestStream struct {
	gogrpc.ServerStream
	ctx      context.Context
	requests []*pb.UploadAttachmentRequest
	response *pb.UploadAttachmentResponse
}

func (s *uploadAttachmentTestStream) Context() context.Context { return s.ctx }

func (s *uploadAttachmentTestStream) Recv() (*pb.UploadAttachmentRequest, error) {
	if len(s.requests) == 0 {
		return nil, io.EOF
	}
	req := s.requests[0]
	s.requests = s.requests[1:]
	return req, nil
}

func (s *uploadAttachmentTestStream) SendAndClose(response *pb.UploadAttachmentResponse) error {
	s.response = response
	return nil
}

func TestGRPCUploadInvalidExpiryDoesNotStoreBlob(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Mode = config.ModeTesting
	cfg.LocalUserID = "alice"
	cfg.UnixSocketAuth = "local"
	resolver, err := security.NewTokenResolver(&cfg)
	require.NoError(t, err)

	provider := &recordingUploadAttachmentProvider{consumeInput: true}
	server := &AttachmentsServer{AttachStore: provider, Config: &cfg, MaxBodySize: 1024}
	stream := &uploadAttachmentTestStream{
		ctx: context.Background(),
		requests: []*pb.UploadAttachmentRequest{
			{Payload: &pb.UploadAttachmentRequest_Metadata{Metadata: &pb.UploadMetadata{
				Filename: "upload.txt", ContentType: "text/plain", ExpiresIn: "not-a-duration",
			}}},
			{Payload: &pb.UploadAttachmentRequest_Chunk{Chunk: []byte("content")}},
		},
	}

	interceptor := security.GRPCStreamInterceptorWithRateLimiter(resolver, nil)
	err = interceptor(server, stream, &gogrpc.StreamServerInfo{IsClientStream: true}, func(_ any, authenticated gogrpc.ServerStream) error {
		stream.ctx = authenticated.Context()
		return server.UploadAttachment(stream)
	})

	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "invalid expires_in value")
	require.Zero(t, provider.storeCalls)
	require.Nil(t, stream.response)
}

func TestGRPCUploadProtocolErrorAfterStoreQueuesCleanup(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Mode = config.ModeTesting
	cfg.LocalUserID = "alice"
	cfg.UnixSocketAuth = "local"
	resolver, err := security.NewTokenResolver(&cfg)
	require.NoError(t, err)

	store := &retryingGRPCAttachmentStore{}
	provider := &recordingUploadAttachmentProvider{}
	server := &AttachmentsServer{Store: store, AttachStore: provider, Config: &cfg, MaxBodySize: 1024}
	metadata := &pb.UploadAttachmentRequest{Payload: &pb.UploadAttachmentRequest_Metadata{Metadata: &pb.UploadMetadata{
		Filename: "upload.txt", ContentType: "text/plain", ExpiresIn: "PT1H",
	}}}
	stream := &uploadAttachmentTestStream{
		ctx:      context.Background(),
		requests: []*pb.UploadAttachmentRequest{metadata, metadata},
	}

	interceptor := security.GRPCStreamInterceptorWithRateLimiter(resolver, nil)
	err = interceptor(server, stream, &gogrpc.StreamServerInfo{IsClientStream: true}, func(_ any, authenticated gogrpc.ServerStream) error {
		stream.ctx = authenticated.Context()
		return server.UploadAttachment(stream)
	})

	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Equal(t, 1, provider.storeCalls)
	require.Len(t, store.tasks, 2, "the detached cleanup transaction is retry-safe")
	for _, task := range store.tasks {
		require.Equal(t, "stored-key", task[registrystore.AttachmentBlobDeleteStorageKeyField])
	}
	require.Nil(t, stream.response)
}

func TestGRPCUploadMetadataFailureAfterStoreQueuesCleanup(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Mode = config.ModeTesting
	cfg.LocalUserID = "alice"
	cfg.UnixSocketAuth = "local"
	resolver, err := security.NewTokenResolver(&cfg)
	require.NoError(t, err)

	store := &retryingGRPCAttachmentStore{createErr: io.ErrClosedPipe}
	provider := &recordingUploadAttachmentProvider{consumeInput: true}
	server := &AttachmentsServer{Store: store, AttachStore: provider, Config: &cfg, MaxBodySize: 1024}
	stream := &uploadAttachmentTestStream{
		ctx: context.Background(),
		requests: []*pb.UploadAttachmentRequest{
			{Payload: &pb.UploadAttachmentRequest_Metadata{Metadata: &pb.UploadMetadata{
				Filename: "upload.txt", ContentType: "text/plain", ExpiresIn: "PT1H",
			}}},
			{Payload: &pb.UploadAttachmentRequest_Chunk{Chunk: []byte("content")}},
		},
	}

	interceptor := security.GRPCStreamInterceptorWithRateLimiter(resolver, nil)
	err = interceptor(server, stream, &gogrpc.StreamServerInfo{IsClientStream: true}, func(_ any, authenticated gogrpc.ServerStream) error {
		stream.ctx = authenticated.Context()
		return server.UploadAttachment(stream)
	})

	require.Equal(t, codes.Internal, status.Code(err))
	require.Equal(t, 1, provider.storeCalls)
	require.Len(t, store.tasks, 2, "the detached cleanup transaction is retry-safe")
	for _, task := range store.tasks {
		require.Equal(t, "stored-key", task[registrystore.AttachmentBlobDeleteStorageKeyField])
	}
	require.Nil(t, stream.response)
}
