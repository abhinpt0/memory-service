package grpc

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/chirino/memory-service/internal/config"
	pb "github.com/chirino/memory-service/internal/generated/pb/memory/v1"
	"github.com/chirino/memory-service/internal/model"
	registryeventbus "github.com/chirino/memory-service/internal/registry/eventbus"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
	"github.com/chirino/memory-service/internal/security"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gogrpc "google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
)

type recordingGRPCEventBus struct {
	mu     sync.Mutex
	events []registryeventbus.Event
}

func (b *recordingGRPCEventBus) Publish(_ context.Context, event registryeventbus.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, event)
	return nil
}

func (b *recordingGRPCEventBus) Subscribe(context.Context, string) (<-chan registryeventbus.Event, error) {
	return nil, nil
}

func (b *recordingGRPCEventBus) Close() error { return nil }

func (b *recordingGRPCEventBus) published() []registryeventbus.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]registryeventbus.Event(nil), b.events...)
}

func invokeLocalGRPC(t *testing.T, req any, handler func(context.Context) (any, error)) (any, error) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Mode = config.ModeTesting
	cfg.LocalUserID = "alice"
	cfg.LocalClientID = "test-client"
	cfg.UnixSocketAuth = "local"
	resolver, err := security.NewTokenResolver(&cfg)
	require.NoError(t, err)
	interceptor := security.GRPCUnaryInterceptorWithRateLimiter(resolver, nil)
	return interceptor(context.Background(), req, &gogrpc.UnaryServerInfo{}, func(ctx context.Context, _ any) (any, error) {
		return handler(ctx)
	})
}

type replayingCreateConversationStore struct {
	registrystore.MemoryStore
	conversation *registrystore.ConversationDetail
	attempts     int
}

func (s *replayingCreateConversationStore) InWriteTx(ctx context.Context, fn func(context.Context) error) error {
	if err := fn(ctx); err != nil {
		return err
	}
	return fn(ctx)
}

func (s *replayingCreateConversationStore) InReadTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *replayingCreateConversationStore) GetGroupMemberUserIDs(context.Context, uuid.UUID) ([]string, error) {
	return []string{"alice"}, nil
}

func (s *replayingCreateConversationStore) CreateConversationWithID(context.Context, string, string, string, string, map[string]any, *string, *string, *uuid.UUID) (*registrystore.CreateConversationResult, error) {
	s.attempts++
	return &registrystore.CreateConversationResult{Conversation: s.conversation, ExactRetry: s.attempts == 2}, nil
}

func (s *replayingCreateConversationStore) OutboxEnabled() bool { return false }

func TestCreateConversationRetryPublishesOnlyCommittedAttemptEvents(t *testing.T) {
	groupID := uuid.New()
	store := &replayingCreateConversationStore{conversation: &registrystore.ConversationDetail{
		ConversationSummary: registrystore.ConversationSummary{ID: "conversation-1", OwnerUserID: "alice", ConversationGroupID: groupID},
	}}
	bus := &recordingGRPCEventBus{}
	server := &ConversationsServer{Store: store, EventBus: bus}
	conversationID := "conversation-1"
	req := &pb.CreateConversationRequest{Id: &conversationID}

	_, err := invokeLocalGRPC(t, req, func(ctx context.Context) (any, error) {
		return server.CreateConversation(ctx, req)
	})
	require.NoError(t, err)
	require.Equal(t, 2, store.attempts)
	require.Empty(t, bus.published(), "the exact-retry attempt committed, so the aborted attempt's created event must not escape")
}

type replayingAppendRecoveryStore struct {
	registrystore.MemoryStore
	conversation *registrystore.ConversationDetail
	entry        model.Entry
	writeTxCalls int
}

func (s *replayingAppendRecoveryStore) InWriteTx(ctx context.Context, fn func(context.Context) error) error {
	s.writeTxCalls++
	if s.writeTxCalls == 1 {
		return fn(ctx)
	}
	if err := fn(ctx); err != nil {
		return err
	}
	return fn(ctx)
}

func (s *replayingAppendRecoveryStore) InReadTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *replayingAppendRecoveryStore) GetGroupMemberUserIDs(context.Context, uuid.UUID) ([]string, error) {
	return []string{"alice"}, nil
}

func (s *replayingAppendRecoveryStore) GetConversation(context.Context, string, string) (*registrystore.ConversationDetail, error) {
	return s.conversation, nil
}

func (s *replayingAppendRecoveryStore) AppendEntries(context.Context, string, string, []registrystore.CreateEntryRequest, *string, *string, *int64) ([]model.Entry, error) {
	return nil, registrystore.NewDuplicateSequenceConflict()
}

func (s *replayingAppendRecoveryStore) GetEntriesBySequence(context.Context, string, string, []uint32) ([]model.Entry, error) {
	return []model.Entry{s.entry}, nil
}

func (s *replayingAppendRecoveryStore) RecoverAppendBatch(context.Context, model.Entry, registrystore.SequencedAppendRequest) (registrystore.SequencedAppendMatch, error) {
	return registrystore.SequencedAppendMatch{Entries: []model.Entry{s.entry}, Created: []model.Entry{s.entry}, Exact: true, AnyExisting: true}, nil
}

func (s *replayingAppendRecoveryStore) GetEntryGroupID(context.Context, uuid.UUID) (uuid.UUID, error) {
	return s.entry.ConversationGroupID, nil
}

func (s *replayingAppendRecoveryStore) OutboxEnabled() bool { return false }

func TestAppendEntriesRecoveryRetryPublishesOnlyFinalAttemptEvents(t *testing.T) {
	seq := uint32(7)
	groupID := uuid.New()
	entry := model.Entry{
		ID:                  uuid.New(),
		ConversationID:      "conversation-1",
		ConversationGroupID: groupID,
		Channel:             model.ChannelContext,
		ContentType:         "application/json",
		Content:             json.RawMessage(`["hello"]`),
		Seq:                 &seq,
		AppendBatch:         []byte{1},
	}
	store := &replayingAppendRecoveryStore{
		conversation: &registrystore.ConversationDetail{ConversationSummary: registrystore.ConversationSummary{
			ID: "conversation-1", OwnerUserID: "alice", ConversationGroupID: groupID, AccessLevel: model.AccessLevelOwner,
		}},
		entry: entry,
	}
	bus := &recordingGRPCEventBus{}
	server := &EntriesServer{Store: store, EventBus: bus}
	req := &pb.AppendEntriesRequest{ConversationId: "conversation-1", Entries: []*pb.CreateEntryRequest{{
		Channel: pb.Channel_CONTEXT, ContentType: "application/json", Content: []*structpb.Value{structpb.NewStringValue("hello")}, Seq: &seq,
	}}}

	resp, err := invokeLocalGRPC(t, req, func(ctx context.Context) (any, error) {
		return server.AppendEntries(ctx, req)
	})
	require.NoError(t, err)
	require.Len(t, resp.(*pb.AppendEntriesResponse).GetEntries(), 1)
	require.Equal(t, 2, store.writeTxCalls)
	require.Len(t, bus.published(), 1, "replaying recovery must replace, not append to, the aborted attempt's events")
}

type replayingSyncEntriesStore struct {
	registrystore.MemoryStore
	conversation *registrystore.ConversationDetail
	entry        model.Entry
	attempts     int
}

func (s *replayingSyncEntriesStore) InWriteTx(ctx context.Context, fn func(context.Context) error) error {
	if err := fn(ctx); err != nil {
		return err
	}
	return fn(ctx)
}

func (s *replayingSyncEntriesStore) InReadTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *replayingSyncEntriesStore) GetGroupMemberUserIDs(context.Context, uuid.UUID) ([]string, error) {
	return []string{"alice"}, nil
}

func (s *replayingSyncEntriesStore) GetConversation(context.Context, string, string) (*registrystore.ConversationDetail, error) {
	return s.conversation, nil
}

func (s *replayingSyncEntriesStore) SyncAgentEntry(context.Context, string, string, registrystore.CreateEntryRequest, string, *string) (*registrystore.SyncResult, error) {
	s.attempts++
	if s.attempts == 1 {
		return &registrystore.SyncResult{Entry: &s.entry}, nil
	}
	return &registrystore.SyncResult{NoOp: true}, nil
}

func (s *replayingSyncEntriesStore) GetEntryGroupID(context.Context, uuid.UUID) (uuid.UUID, error) {
	return s.entry.ConversationGroupID, nil
}

func (s *replayingSyncEntriesStore) OutboxEnabled() bool { return false }

func TestSyncEntriesRetryPublishesOnlyCommittedAttemptEvents(t *testing.T) {
	groupID := uuid.New()
	store := &replayingSyncEntriesStore{
		conversation: &registrystore.ConversationDetail{ConversationSummary: registrystore.ConversationSummary{
			ID: "conversation-1", OwnerUserID: "alice", ConversationGroupID: groupID, AccessLevel: model.AccessLevelOwner,
		}},
		entry: model.Entry{ID: uuid.New(), ConversationID: "conversation-1", ConversationGroupID: groupID, Channel: model.ChannelContext, ContentType: "application/json", Content: json.RawMessage(`[]`)},
	}
	bus := &recordingGRPCEventBus{}
	server := &EntriesServer{Store: store, EventBus: bus}
	req := &pb.SyncEntriesRequest{ConversationId: "conversation-1", Entry: &pb.CreateEntryRequest{
		Channel: pb.Channel_CONTEXT, ContentType: "application/json", Content: []*structpb.Value{structpb.NewStringValue("hello")},
	}}

	resp, err := invokeLocalGRPC(t, req, func(ctx context.Context) (any, error) {
		return server.SyncEntries(ctx, req)
	})
	require.NoError(t, err)
	require.True(t, resp.(*pb.SyncEntriesResponse).GetNoOp())
	require.Equal(t, 2, store.attempts)
	require.Empty(t, bus.published(), "the committed no-op attempt must not publish the aborted attempt's entry event")
}
