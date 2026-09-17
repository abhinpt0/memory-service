package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConversationSortCursorRoundTrip(t *testing.T) {
	sort, err := ParseConversationSort(stringPtr("updatedAt"), stringPtr("asc"))
	require.NoError(t, err)

	summary := ConversationSummary{
		ID:        "conversation-1",
		CreatedAt: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 9, 18, 13, 30, 0, 123, time.UTC),
	}
	token, err := EncodeConversationCursor(summary, sort)
	require.NoError(t, err)
	require.Contains(t, token, conversationCursorPrefix)

	cursor, legacy, err := ParseConversationCursor(token, sort)
	require.NoError(t, err)
	require.False(t, legacy)
	require.Equal(t, summary.ID, cursor.ID)
	require.Equal(t, summary.UpdatedAt, cursor.Value)
}

func TestConversationSortCursorRejectsDifferentSort(t *testing.T) {
	updatedSort, err := ParseConversationSort(stringPtr("updatedAt"), stringPtr("desc"))
	require.NoError(t, err)
	token, err := EncodeConversationCursor(ConversationSummary{
		ID:        "conversation-1",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}, updatedSort)
	require.NoError(t, err)

	createdSort, err := ParseConversationSort(stringPtr("createdAt"), stringPtr("desc"))
	require.NoError(t, err)
	_, _, err = ParseConversationCursor(token, createdSort)
	require.ErrorContains(t, err, "does not match")
}

func TestConversationSortLegacyCursorOnlyForImplicitDefault(t *testing.T) {
	defaultSort := DefaultConversationSort()
	_, legacy, err := ParseConversationCursor("conversation-1", defaultSort)
	require.NoError(t, err)
	require.True(t, legacy)
	_, legacy, err = ParseConversationCursor("cs1.arbitrary-conversation-id", defaultSort)
	require.NoError(t, err)
	require.True(t, legacy)

	explicitSort, err := ParseConversationSort(stringPtr("createdAt"), stringPtr("desc"))
	require.NoError(t, err)
	_, _, err = ParseConversationCursor("conversation-1", explicitSort)
	require.Error(t, err)
}

func stringPtr(value string) *string {
	return &value
}
