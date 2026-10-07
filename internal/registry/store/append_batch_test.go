package store

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/chirino/memory-service/internal/model"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAppendBatchMatchesCompleteDefinition(t *testing.T) {
	seq := uint32(10)
	r := SequencedAppendRequest{UserID: "alice", Entries: []CreateEntryRequest{
		{Channel: "history", ContentType: "history", Content: []byte(`[{"role":"USER","text":"prefix"}]`)},
		{Channel: "journal", ContentType: "test", Seq: &seq, Content: []byte(`{"number":1}`)},
		{Channel: "history", ContentType: "history", Content: []byte(`[{"role":"USER","text":"suffix"}]`)},
	}}
	data, err := EncodeAppendBatch(context.Background(), nil, NewAppendBatch(r, time.Now()))
	require.NoError(t, err)
	b, err := DecodeAppendBatch(data)
	require.NoError(t, err)
	retry := r
	retry.Entries = append([]CreateEntryRequest(nil), r.Entries...)
	retry.Entries[1].Content = []byte(`{"number":1.0}`)
	equal, err := b.Matches(context.Background(), nil, retry)
	require.NoError(t, err)
	require.True(t, equal)
	retry.Entries[1].Content = []byte(`{"number":2}`)
	equal, err = b.Matches(context.Background(), nil, retry)
	require.NoError(t, err)
	require.False(t, equal)
	retry = r
	retry.Entries = r.Entries[:2]
	equal, err = b.Matches(context.Background(), nil, retry)
	require.NoError(t, err)
	require.False(t, equal)
	retry = r
	retry.Entries = append([]CreateEntryRequest(nil), r.Entries...)
	retry.Entries[0], retry.Entries[2] = retry.Entries[2], retry.Entries[0]
	equal, err = b.Matches(context.Background(), nil, retry)
	require.NoError(t, err)
	require.False(t, equal)
}

func TestNextAppendTimeRespectsPersistedPrecisionAndFutureTail(t *testing.T) {
	latest := time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC)
	now := latest.Add(-time.Second)
	next := NextAppendTime(now, latest)
	require.True(t, next.After(latest))
	require.Equal(t, 0, next.Nanosecond()%int(time.Millisecond))
	require.Equal(t, latest.Truncate(time.Millisecond).Add(time.Millisecond), next)
}

func TestAppendBatchDefinitionDoesNotDuplicateLargeContent(t *testing.T) {
	seq := uint32(10)
	text := strings.Repeat("x", 2*1024*1024)
	content := json.RawMessage(`[{"role":"USER","text":"` + text + `"}]`)
	r := SequencedAppendRequest{UserID: "alice", Entries: make([]CreateEntryRequest, 10)}
	for i := range r.Entries {
		r.Entries[i] = CreateEntryRequest{Channel: "history", ContentType: "history", Content: content, IndexedContent: &text}
	}
	r.Entries[0].Seq = &seq
	data, err := EncodeAppendBatch(context.Background(), nil, NewAppendBatch(r, time.Now()))
	require.NoError(t, err)
	require.Less(t, len(data), 4096)
	batch, err := DecodeAppendBatch(data)
	require.NoError(t, err)
	equal, err := batch.Matches(context.Background(), nil, r)
	require.NoError(t, err)
	require.True(t, equal)
}

func TestAppendBatchRestoresOriginalAttachmentClone(t *testing.T) {
	sourceID, cloneID := uuid.New(), uuid.New()
	key := "same-storage-key"
	fake := &batchAttachmentStore{attachments: map[uuid.UUID]*model.Attachment{sourceID: {ID: sourceID, StorageKey: &key}, cloneID: {ID: cloneID, StorageKey: &key}}}
	seq := uint32(10)
	original := SequencedAppendRequest{UserID: "alice", Entries: []CreateEntryRequest{
		{Channel: "journal", ContentType: "test", Seq: &seq, Content: []byte(`[]`)},
		{Channel: "history", ContentType: "history", Content: []byte(fmt.Sprintf(`[{"attachments":[{"attachmentId":%q}]}]`, cloneID))},
	}}
	data, err := EncodeAppendBatch(context.Background(), fake, NewAppendBatch(original, time.Now()))
	require.NoError(t, err)
	batch, err := DecodeAppendBatch(data)
	require.NoError(t, err)
	retry := original
	retry.Entries = append([]CreateEntryRequest(nil), original.Entries...)
	retry.Entries[1].Content = []byte(fmt.Sprintf(`[{"attachments":[{"attachmentId":%q}]}]`, sourceID))
	equal, err := batch.Matches(context.Background(), fake, retry)
	require.NoError(t, err)
	require.True(t, equal)
	restored, err := batch.RestoreRequest(retry)
	require.NoError(t, err)
	require.JSONEq(t, string(original.Entries[1].Content), string(restored.Entries[1].Content))
	retry.Entries[1].Content = []byte(fmt.Sprintf(`[{"attachments":[{"attachmentId":{"storageKey":%q}}]}]`, key))
	equal, err = batch.Matches(context.Background(), fake, retry)
	require.NoError(t, err)
	require.False(t, equal)
}

type batchAttachmentStore struct {
	MemoryStore
	attachments map[uuid.UUID]*model.Attachment
}

func (s *batchAttachmentStore) GetAttachment(_ context.Context, _ string, _ string, id uuid.UUID) (*model.Attachment, error) {
	return s.attachments[id], nil
}
