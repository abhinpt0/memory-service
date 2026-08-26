package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/chirino/memory-service/internal/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEpochForChannelOnlySetsContextEpoch(t *testing.T) {
	epoch := int64(7)

	assert.Nil(t, EpochForChannel(model.ChannelHistory, &epoch))
	assert.Nil(t, EpochForChannel(model.ChannelJournal, &epoch))
	require.NotNil(t, EpochForChannel(model.ChannelContext, &epoch))
	assert.Equal(t, epoch, *EpochForChannel(model.ChannelContext, &epoch))
	assert.Equal(t, int64(1), *EpochForChannel(model.ChannelContext, nil))
}

func TestSequencedAppendRequestRejectsMismatchAndUnsequencedRequest(t *testing.T) {
	userID := "alice"
	seq1 := uint32(1)
	request := SequencedAppendRequest{
		Entries: []CreateEntryRequest{{Channel: "history", ContentType: "history", Content: json.RawMessage(`[{"text":"one"}]`), Seq: &seq1}},
		UserID:  userID,
	}
	stored := []model.Entry{{UserID: &userID, Channel: model.ChannelHistory, Seq: &seq1, ContentType: "history", Content: []byte(`[{"text":"different"}]`)}}
	_, ok := request.MatchExistingEntries(stored)
	require.False(t, ok)

	request.Entries[0].Seq = nil
	_, eligible := request.Sequences()
	require.False(t, eligible)
}

func TestSequencedAppendRequestDoesNotRoundLargeJSONNumbers(t *testing.T) {
	userID := "alice"
	seq := uint32(1)
	request := SequencedAppendRequest{
		Entries: []CreateEntryRequest{{
			Channel: "context", ContentType: "test.v1", Seq: &seq,
			Content: json.RawMessage(`[{"value":9007199254740993}]`),
		}},
		UserID: userID,
	}
	stored := []model.Entry{{
		UserID: &userID, Channel: model.ChannelContext, Seq: &seq,
		Epoch: int64Ptr(1), ContentType: "test.v1",
		Content: []byte(`[{"value":9007199254740992}]`),
	}}

	_, ok := request.MatchExistingEntries(stored)
	require.False(t, ok)
}

func TestJSONValuesEqualFailsClosedOnHugeExponents(t *testing.T) {
	require.False(t, jsonValuesEqual([]byte(`[1e1000000]`), []byte(`[10e999999]`)))
	allocations := testing.AllocsPerRun(1000, func() {
		_, ok := normalizeJSONNumber(json.Number("1e1000000"))
		require.False(t, ok)
	})
	require.Zero(t, allocations)
	require.True(t, jsonValuesEqual([]byte(`[1]`), []byte(`[1.0]`)))
	require.True(t, jsonValuesEqual([]byte(`[1]`), []byte(`[1e0]`)))
}

func TestAttachmentIdentityFallbackIgnoresArbitraryAttachmentIDKeys(t *testing.T) {
	left := []byte(`[{"tool":{"attachmentId":"00000000-0000-4000-8000-000000000001"}}]`)
	right := []byte(`[{"tool":{"attachmentId":"00000000-0000-4000-8000-000000000002"}}]`)
	equal, err := jsonValuesEqualByAttachmentIdentity(context.Background(), nil, "alice", left, right)
	require.NoError(t, err)
	require.False(t, equal)
}

func TestSequencedAppendRequestMatchesPersistedConversationLineage(t *testing.T) {
	seq := uint32(1)
	parent := "parent"
	otherParent := "other-parent"
	anchor := uuid.New()
	request := SequencedAppendRequest{Entries: []CreateEntryRequest{{
		Seq: &seq, ForkedAtConversationID: &parent, ForkedAtEntryID: &anchor,
	}}}
	conversation := &ConversationDetail{ConversationSummary: ConversationSummary{
		ForkedAtConversationID: &parent, ForkedAtEntryID: &anchor,
	}}
	require.True(t, request.lineageMatches(conversation))

	request.Entries[0].ForkedAtConversationID = &otherParent
	require.False(t, request.lineageMatches(conversation))
}

func int64Ptr(value int64) *int64 { return &value }

func TestPaginateEntriesRejectsUnknownAfterCursor(t *testing.T) {
	entries := []model.Entry{{ID: uuid.New()}, {ID: uuid.New()}}
	missing := uuid.NewString()

	_, _, _, err := PaginateEntries(entries, &missing, nil, false, 1)
	require.EqualError(t, err, "afterCursor entry not found in visible results")
}

func TestValidateEntryEpochChannelsRejectsNonContextEntries(t *testing.T) {
	epoch := int64(2)

	err := ValidateEntryEpochChannels([]CreateEntryRequest{{Channel: "history"}}, &epoch)
	require.EqualError(t, err, `epoch can only be set for context entries; entry channel "history" does not support epochs`)

	err = ValidateEntryEpochChannels([]CreateEntryRequest{{Channel: "journal"}}, &epoch)
	require.EqualError(t, err, `epoch can only be set for context entries; entry channel "journal" does not support epochs`)

	require.NoError(t, ValidateEntryEpochChannels([]CreateEntryRequest{{Channel: "context"}}, &epoch))
	require.NoError(t, ValidateEntryEpochChannels([]CreateEntryRequest{{Channel: "history"}}, nil))
}

func TestEntryLookupQueriesAreBoundedToTarget(t *testing.T) {
	entryID := uuid.New()
	channel := model.ChannelHistory
	clientID := "client-1"

	query := EntryLookupQuery(entryID, &channel, &clientID)
	require.Equal(t, 1, query.Limit)
	require.True(t, query.Tail)
	require.True(t, query.AllForks)
	require.Equal(t, entryID.String(), requireStringValue(t, query.UpToEntryID))
	require.Equal(t, channel, *query.Channel)
	require.Equal(t, clientID, *query.ClientID)

	adminQuery := AdminEntryLookupQuery(entryID)
	require.Equal(t, 1, adminQuery.Limit)
	require.True(t, adminQuery.Tail)
	require.True(t, adminQuery.AllForks)
	require.Equal(t, entryID.String(), requireStringValue(t, adminQuery.UpToEntryID))
}

func requireStringValue(t *testing.T, value *string) string {
	t.Helper()
	require.NotNil(t, value)
	return *value
}
