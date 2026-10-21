package search

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	registrystore "github.com/chirino/memory-service/internal/registry/store"
	"github.com/chirino/memory-service/internal/security"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type retryingIndexStore struct {
	registrystore.MemoryStore
	indexed [][]registrystore.IndexEntryRequest
}

func (s *retryingIndexStore) InWriteTx(ctx context.Context, fn func(context.Context) error) error {
	if err := fn(ctx); err != nil {
		return err
	}
	return fn(ctx)
}

func (s *retryingIndexStore) IndexEntries(_ context.Context, entries []registrystore.IndexEntryRequest) (*registrystore.IndexConversationsResponse, error) {
	s.indexed = append(s.indexed, append([]registrystore.IndexEntryRequest(nil), entries...))
	return &registrystore.IndexConversationsResponse{Indexed: len(entries)}, nil
}

func TestIndexConversationsRetryReusesParsedRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	entryID := uuid.New()
	body, err := json.Marshal([]registrystore.IndexEntryRequest{{
		EntryID: entryID, ConversationID: "conversation-1", IndexedContent: "same on every attempt",
	}})
	require.NoError(t, err)
	store := &retryingIndexStore{}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/index", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(security.ContextKeyRoles, map[string]bool{security.RoleIndexer: true})

	indexConversations(c, store)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Len(t, store.indexed, 2)
	require.Equal(t, store.indexed[0], store.indexed[1])
	require.Equal(t, entryID, store.indexed[1][0].EntryID)
}

func TestNormalizeSearchTypes(t *testing.T) {
	t.Run("nil defaults to auto", func(t *testing.T) {
		got, err := normalizeSearchTypes(nil)
		require.NoError(t, err)
		require.Equal(t, []string{searchTypeAuto}, got)
	})

	t.Run("single value", func(t *testing.T) {
		got, err := normalizeSearchTypes("semantic")
		require.NoError(t, err)
		require.Equal(t, []string{searchTypeSemantic}, got)
	})

	t.Run("array deduplicates", func(t *testing.T) {
		got, err := normalizeSearchTypes([]any{"semantic", "fulltext", "semantic"})
		require.NoError(t, err)
		require.Equal(t, []string{searchTypeSemantic, searchTypeFulltext}, got)
	})

	t.Run("auto cannot be combined", func(t *testing.T) {
		_, err := normalizeSearchTypes([]any{"auto", "fulltext"})
		require.ErrorContains(t, err, "cannot be combined")
	})

	t.Run("non string array value rejected", func(t *testing.T) {
		_, err := normalizeSearchTypes([]any{"semantic", 42})
		require.ErrorContains(t, err, "must be strings")
	})
}

func TestDecodeAfterCursor(t *testing.T) {
	t.Run("decode typed cursor token", func(t *testing.T) {
		encoded := encodeAfterCursor(
			[]string{searchTypeSemantic, searchTypeFulltext},
			map[string]string{
				searchTypeSemantic: "11111111-1111-1111-1111-111111111111",
				searchTypeFulltext: "22222222-2222-2222-2222-222222222222",
			},
		)
		require.NotNil(t, encoded)

		got, types, err := decodeAfterCursor(encoded)
		require.NoError(t, err)
		require.Equal(t, []string{searchTypeSemantic, searchTypeFulltext}, types)
		require.Equal(t, "11111111-1111-1111-1111-111111111111", got[searchTypeSemantic])
		require.Equal(t, "22222222-2222-2222-2222-222222222222", got[searchTypeFulltext])
	})

	t.Run("plain cursor rejected", func(t *testing.T) {
		raw := "not-a-valid-token"
		_, _, err := decodeAfterCursor(&raw)
		require.ErrorContains(t, err, errInvalidAfterCursor.Error())
	})
}

func TestSemanticCandidateLimit(t *testing.T) {
	require.Equal(t, 11, semanticCandidateLimit(10, false, nil))
	require.Equal(t, 31, semanticCandidateLimit(10, true, nil))

	cursor := "44444444-4444-4444-4444-444444444444"
	require.Equal(t, 1000, semanticCandidateLimit(10, false, &cursor))
	require.Equal(t, 5000, semanticCandidateLimit(4000, true, nil))
}

func TestPaginateSearchResults(t *testing.T) {
	id1 := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	id2 := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	id3 := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	results := []registrystore.SearchResult{
		{EntryID: id1},
		{EntryID: id2},
		{EntryID: id3},
	}

	page, next, err := paginateSearchResults(results, nil, 2)
	require.NoError(t, err)
	require.Len(t, page, 2)
	require.NotNil(t, next)
	require.Equal(t, id2.String(), *next)

	page2, next2, err := paginateSearchResults(results, next, 2)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	require.Equal(t, id3, page2[0].EntryID)
	require.Nil(t, next2)
}
