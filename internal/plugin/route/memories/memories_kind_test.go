package memories

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chirino/memory-service/internal/config"
	"github.com/chirino/memory-service/internal/model"
	registryembed "github.com/chirino/memory-service/internal/registry/embed"
	registryepisodic "github.com/chirino/memory-service/internal/registry/episodic"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type resolveCountingRESTStore struct {
	registryepisodic.EpisodicStore
	resolveCalls    int
	putRequest      registryepisodic.PutMemoryRequest
	txCallbacks     int
	txCallbackCount int
	txErr           error
}

func (s *resolveCountingRESTStore) InWriteTx(ctx context.Context, fn func(context.Context) error) error {
	callbacks := s.txCallbacks
	if callbacks == 0 {
		callbacks = 1
	}
	for range callbacks {
		s.txCallbackCount++
		if err := fn(ctx); err != nil {
			return err
		}
	}
	return s.txErr
}

func (s *resolveCountingRESTStore) ResolveKindForWrite(context.Context, string) (string, error) {
	s.resolveCalls++
	if s.resolveCalls == 1 {
		return "resolved/v1", nil
	}
	return "changed/v1", nil
}

func (s *resolveCountingRESTStore) GetMemoryKindVersion(_ context.Context, name string) (*model.MemoryKindVersion, error) {
	return &model.MemoryKindVersion{Name: name, AttributeTypes: map[string]string{}, Writable: true}, nil
}

func (s *resolveCountingRESTStore) PutMemory(_ context.Context, req registryepisodic.PutMemoryRequest) (*registryepisodic.MemoryWriteResult, error) {
	s.putRequest = req
	return &registryepisodic.MemoryWriteResult{
		ID: reqID, Namespace: req.Namespace, Key: req.Key, MemoryKind: req.MemoryKind, Revision: 1, CreatedAt: time.Now().UTC(),
	}, nil
}

var reqID = uuid.MustParse("9b141ab5-2e91-4ea3-8eb3-6c193062c6aa")

func TestRESTPutResolvesKindExactlyOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &resolveCountingRESTStore{}
	body, err := json.Marshal(map[string]interface{}{
		"namespace": []string{"user", "alice", "single-resolve"},
		"key":       "key",
		"value":     map[string]interface{}{"note": "value"},
	})
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/v1/memories", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	cfg := config.DefaultConfig()
	putMemory(c, store, nil, &cfg)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, 1, store.resolveCalls)
	require.Equal(t, "resolved/v1", store.putRequest.MemoryKind)
}

func TestRESTPutDoesNotRenderSuccessWhenCommitFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &resolveCountingRESTStore{txErr: errors.New("commit failed")}
	recorder := executeRESTPut(t, store)

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.NotContains(t, recorder.Body.String(), reqID.String())
}

func TestRESTPutRendersOnceWhenTransactionCallbackRetries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &resolveCountingRESTStore{txCallbacks: 2}
	recorder := executeRESTPut(t, store)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Equal(t, 2, store.txCallbackCount)
	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &decoded), recorder.Body.String())
	require.Equal(t, reqID.String(), decoded["id"])
}

func executeRESTPut(t *testing.T, store registryepisodic.EpisodicStore) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{
		"namespace": []string{"user", "alice", "commit-boundary"},
		"key":       "key",
		"value":     map[string]interface{}{"note": "value"},
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPut, "/v1/memories", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	cfg := config.DefaultConfig()
	putMemory(c, store, nil, &cfg)
	return recorder
}

// fakeEmbedder returns a zero-length float32 slice per query (sufficient for
// unit tests that only check kind/stale filtering, not embedding quality).
type fakeEmbedder struct{}

func (f *fakeEmbedder) EmbedTexts(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{0.1, 0.2, 0.3}
	}
	return out, nil
}

func (f *fakeEmbedder) Dimension() int    { return 3 }
func (f *fakeEmbedder) ModelName() string { return "fake" }

var _ registryembed.Embedder = (*fakeEmbedder)(nil)

var theEmbedder registryembed.Embedder = &fakeEmbedder{}

// --- Defect 6: matchesKindSelector for kind top-k filtering ---

func TestMatchesKindSelectorEmptyMatchesAll(t *testing.T) {
	t.Parallel()
	cases := []string{"events/v1", "events/v2", "custom/schema", "default/v1"}
	for _, schema := range cases {
		if !matchesKindSelector(schema, "") {
			t.Errorf("matchesKindSelector(%q, \"\") = false, want true", schema)
		}
	}
}

func TestMatchesKindSelectorExactMatchOnly(t *testing.T) {
	t.Parallel()
	if !matchesKindSelector("events/v1", "events/v1") {
		t.Error("exact match should return true")
	}
	if matchesKindSelector("events/v2", "events/v1") {
		t.Error("different version should return false")
	}
	if matchesKindSelector("other/v1", "events/v1") {
		t.Error("different family should return false")
	}
}

func TestMatchesKindSelectorFamilyPrefix(t *testing.T) {
	t.Parallel()
	// "events" selector should match "events/v1", "events/v2" but not "events-old/v1"
	if !matchesKindSelector("events/v1", "events") {
		t.Error("family selector should match events/v1")
	}
	if !matchesKindSelector("events/v2", "events") {
		t.Error("family selector should match events/v2")
	}
	// Must not match a sibling family that starts with the same prefix.
	if matchesKindSelector("eventstore/v1", "events") {
		t.Error("family selector must not match eventstore/v1 for selector 'events'")
	}
	if matchesKindSelector("other/v1", "events") {
		t.Error("cross-family exact kind should not match 'events' family selector")
	}
}

// TestMatchesKindSelectorEmptyItemSchemaDoesNotMatch verifies that an empty item schema
// does not match any selector (fresh-only design: all rows have a kind).
func TestMatchesKindSelectorEmptyItemSchemaDoesNotMatch(t *testing.T) {
	t.Parallel()
	// Empty schema must NOT match any specific selector.
	if matchesKindSelector("", "default/v1") {
		t.Error("empty schema must not match specific exact selector")
	}
	if matchesKindSelector("", "default") {
		t.Error("empty schema must not match family selector")
	}
	// Empty selector (all) still matches non-empty schemas.
	if !matchesKindSelector("default/v1", "") {
		t.Error("non-empty schema must match empty selector (all)")
	}
}

// --- fakeEpisodicStore for semantic search unit tests ---

// fakeEpisodicStore is a minimal EpisodicStore stub that returns configurable SearchMemoryVectors
// responses and identity GetMemoriesByIDs results.
type fakeEpisodicStore struct {
	registryepisodic.EpisodicStore // embed nil interface for unimplemented methods
	// vectorPages is a list of []MemoryVectorSearch to return sequentially for each call.
	// After the last page the store returns [] forever.
	vectorPages [][]registryepisodic.MemoryVectorSearch
	callCount   int
	// kindByID maps memory IDs to their canonical kind for GetMemoriesByIDs.
	// Use an instance-local map to avoid shared-state races between parallel tests.
	kindByID map[uuid.UUID]string
}

func (f *fakeEpisodicStore) SearchMemoryVectors(_ context.Context, _ string, _ []float32, _ registryepisodic.AttributeFilter, _ string, limit int, _ registryepisodic.ArchiveFilter) ([]registryepisodic.MemoryVectorSearch, error) {
	if f.callCount >= len(f.vectorPages) {
		return nil, nil
	}
	page := f.vectorPages[f.callCount]
	f.callCount++
	// Honour the limit: return at most `limit` results.
	if len(page) > limit {
		page = page[:limit]
	}
	return page, nil
}

func (f *fakeEpisodicStore) GetMemoriesByIDs(_ context.Context, ids []uuid.UUID, _ registryepisodic.ArchiveFilter) ([]registryepisodic.MemoryItem, error) {
	items := make([]registryepisodic.MemoryItem, 0, len(ids))
	for _, id := range ids {
		var kind string
		if f.kindByID != nil {
			kind = f.kindByID[id]
		}
		items = append(items, registryepisodic.MemoryItem{
			ID:         id,
			MemoryKind: kind,
			Revision:   1,
		})
	}
	return items, nil
}

func (f *fakeEpisodicStore) InReadTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}
func (f *fakeEpisodicStore) InWriteTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}
func (f *fakeEpisodicStore) ListMemoryKindVersions(_ context.Context, _ string) ([]model.MemoryKindVersion, error) {
	return nil, nil
}

// --- Bug 3: single-query cap-break ---

// TestSemanticSearchSingleQueryBreaksAtCap verifies that when the backend consistently returns
// exactly semanticSearchMaxOverfetch results (= cap), the loop terminates instead of spinning.
func TestSemanticSearchSingleQueryBreaksAtCap(t *testing.T) {
	t.Parallel()

	// Build cap-many pages, each exactly semanticSearchMaxOverfetch rows, all stale.
	// With the fix the loop must break after the first round at the hard cap.
	capSize := semanticSearchMaxOverfetch
	capPage := make([]registryepisodic.MemoryVectorSearch, capSize)
	for i := range capPage {
		id := uuid.New()
		capPage[i] = registryepisodic.MemoryVectorSearch{
			MemoryID:   id,
			Score:      float64(capSize - i),
			MemoryKind: "events/v1",
			// PrimaryValidated=false means stale checks apply; revision mismatch discards all.
			MemoryRevision:   int64(i + 100), // non-zero
			PrimaryValidated: false,
		}
	}
	// All items have stale revisions — they will be discarded.
	// Provide two pages to show the loop would have looped but doesn't.
	store := &fakeEpisodicStore{
		vectorPages: [][]registryepisodic.MemoryVectorSearch{capPage, capPage},
	}
	// Override GetMemoriesByIDs to return revision=1 (mismatch with 100+i).
	store2 := &staleRevisionStore{fakeBase: store}

	ctx := context.Background()
	results, err := semanticSearchWithKindFilter(ctx, store2, "", nil, registryepisodic.AttributeFilter{}, 10, registryepisodic.ArchiveFilterInclude, "events/v1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should have broken out — 0 results but no infinite loop.
	// From limit=10, fetchLimit doubles: 40, 80, 160, 320, 640, 1000 (at cap).
	// After the cap round, fetchLimit would double to 2000 but is clamped back to 1000,
	// triggering the cap-break.  Total calls ≤ 7 (add a small margin for safety).
	// Before the Bug 3 fix this would loop forever.
	const maxAllowedCalls = 7
	if store2.fakeBase.callCount > maxAllowedCalls {
		t.Errorf("loop did not break at cap: callCount=%d, want ≤%d", store2.fakeBase.callCount, maxAllowedCalls)
	}
	if store2.fakeBase.callCount == 0 {
		t.Error("store was never called")
	}
	_ = results
}

// staleRevisionStore wraps fakeEpisodicStore but returns revision=1 for all items.
type staleRevisionStore struct {
	registryepisodic.EpisodicStore // embed nil interface for unimplemented methods
	fakeBase                       *fakeEpisodicStore
}

func (s *staleRevisionStore) SearchMemoryVectors(ctx context.Context, ns string, embedding []float32, filter registryepisodic.AttributeFilter, memoryKind string, limit int, archived registryepisodic.ArchiveFilter) ([]registryepisodic.MemoryVectorSearch, error) {
	return s.fakeBase.SearchMemoryVectors(ctx, ns, embedding, filter, memoryKind, limit, archived)
}

func (s *staleRevisionStore) GetMemoriesByIDs(_ context.Context, ids []uuid.UUID, _ registryepisodic.ArchiveFilter) ([]registryepisodic.MemoryItem, error) {
	items := make([]registryepisodic.MemoryItem, 0, len(ids))
	for _, id := range ids {
		items = append(items, registryepisodic.MemoryItem{
			ID:       id,
			Revision: 1, // always 1 — mismatches 100+i in the vector result
		})
	}
	return items, nil
}

func (s *staleRevisionStore) InReadTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (s *staleRevisionStore) InWriteTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (s *staleRevisionStore) ListMemoryKindVersions(_ context.Context, _ string) ([]model.MemoryKindVersion, error) {
	return nil, nil
}

// --- Bugs 4+5: multi-query overfetch / legacy validation ---

// TestMultiQuerySemanticSearchReturnsHigherRankedNonmatch verifies that items ranked higher
// in the vector results but belonging to the wrong kind do NOT consume per-query quota and
// that lower-ranked correct-kind items are still returned.
func TestMultiQuerySemanticSearchKindFilterPreservesLowerRanked(t *testing.T) {
	t.Parallel()

	wantID := uuid.New()
	wrongID := uuid.New()

	// wrongID is rank 1 (highest score) but has wrong kind "other/v1".
	// wantID is rank 2 but has the correct kind "events/v1".
	// PrimaryValidated=true: the fakeEpisodicStore's GetMemoriesByIDs returns MemoryKind
	// matching the vector metadata for the validation check in multiQuerySemanticSearch.
	page := []registryepisodic.MemoryVectorSearch{
		{MemoryID: wrongID, Score: 0.99, MemoryKind: "other/v1", PrimaryValidated: true},
		{MemoryID: wantID, Score: 0.88, MemoryKind: "events/v1", PrimaryValidated: true},
	}
	store := &fakeEpisodicStore{
		vectorPages: [][]registryepisodic.MemoryVectorSearch{page},
		kindByID:    map[uuid.UUID]string{wantID: "events/v1", wrongID: "other/v1"},
	}

	queries := []searchQuerySpec{
		{Text: "q1", Purpose: "purpose1"},
	}

	ctx := context.Background()
	results, err := multiQuerySemanticSearch(ctx, store, theEmbedder, nil, registryepisodic.AttributeFilter{}, queries, 5, 5, registryepisodic.ArchiveFilterInclude, "events/v1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, r := range results {
		if r.ID == wrongID {
			t.Error("wrong-kind item should not appear in results")
		}
	}
	found := false
	for _, r := range results {
		if r.ID == wantID {
			found = true
		}
	}
	if !found {
		t.Errorf("expected wantID (%v) in results, got %d items", wantID, len(results))
	}
}

// TestMultiQuerySemanticSearchEmptyVectorSchemaAlwaysRejected verifies the
// fresh-only kind invariant: a vector point with empty MemoryKind is rejected
// regardless of what the primary row kind is.  Under the fresh-only design every
// persisted row carries a non-empty canonical kind; an empty vector schema means
// the vector index is stale and must be reindexed before results are served.
func TestMultiQuerySemanticSearchEmptyVectorSchemaAlwaysRejected(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	// Both vector and primary have empty MemoryKind (impossible in fresh deployment,
	// but even if it were present the vector entry should be rejected as stale).
	page := []registryepisodic.MemoryVectorSearch{
		{MemoryID: id, Score: 0.9, MemoryKind: "", PrimaryValidated: false},
	}
	store := &fakeEpisodicStore{
		vectorPages: [][]registryepisodic.MemoryVectorSearch{page},
		kindByID:    map[uuid.UUID]string{id: ""}, // fresh-only invariant: never empty in prod
	}

	queries := []searchQuerySpec{{Text: "q", Purpose: "p"}}
	ctx := context.Background()
	results, err := multiQuerySemanticSearch(ctx, store, theEmbedder, nil, registryepisodic.AttributeFilter{}, queries, 5, 5, registryepisodic.ArchiveFilterInclude, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, r := range results {
		if r.ID == id {
			t.Error("item with empty vector schema must always be rejected as stale (fresh-only invariant)")
		}
	}
}

// TestMultiQuerySemanticSearchLegacyVectorRejectedWhenPrimaryMigrated verifies Bug 5:
// a vector point with empty MemoryKind is rejected when the primary row has a non-legacy kind.
func TestMultiQuerySemanticSearchLegacyVectorRejectedWhenPrimaryMigrated(t *testing.T) {
	t.Parallel()

	migratedID := uuid.New()
	// Primary has moved to "events/v1" but vector still has empty schema (not yet re-indexed).
	page := []registryepisodic.MemoryVectorSearch{
		{MemoryID: migratedID, Score: 0.9, MemoryKind: "", PrimaryValidated: false},
	}
	store := &fakeEpisodicStore{
		vectorPages: [][]registryepisodic.MemoryVectorSearch{page},
		kindByID:    map[uuid.UUID]string{migratedID: "events/v1"},
	}

	queries := []searchQuerySpec{{Text: "q", Purpose: "p"}}
	ctx := context.Background()
	results, err := multiQuerySemanticSearch(ctx, store, theEmbedder, nil, registryepisodic.AttributeFilter{}, queries, 5, 5, registryepisodic.ArchiveFilterInclude, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, r := range results {
		if r.ID == migratedID {
			t.Error("migrated item with stale empty-schema vector should be rejected")
		}
	}
}

// TestMultiQuerySemanticSearchSQLValidatedItemsSkipStalenessCheck verifies that items
// with PrimaryValidated=true are included regardless of revision/kind metadata.
func TestMultiQuerySemanticSearchSQLValidatedSkipStaleness(t *testing.T) {
	t.Parallel()

	validatedID := uuid.New()
	// PrimaryValidated=true — even though MemoryKind/revision mismatch, item must survive.
	page := []registryepisodic.MemoryVectorSearch{
		{MemoryID: validatedID, Score: 0.9, MemoryKind: "old/v1", MemoryRevision: 99, PrimaryValidated: true},
	}
	store := &fakeEpisodicStore{
		vectorPages: [][]registryepisodic.MemoryVectorSearch{page},
		kindByID:    map[uuid.UUID]string{validatedID: "events/v1"},
	}

	queries := []searchQuerySpec{{Text: "q", Purpose: "p"}}
	ctx := context.Background()
	results, err := multiQuerySemanticSearch(ctx, store, theEmbedder, nil, registryepisodic.AttributeFilter{}, queries, 5, 5, registryepisodic.ArchiveFilterInclude, "events/v1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, r := range results {
		if r.ID == validatedID {
			found = true
		}
	}
	if !found {
		t.Errorf("SQL-validated item should bypass stale checks and be included; got %d results", len(results))
	}
}

// TestMultiQuerySemanticSearchSQLValidatedWrongKindExcluded verifies Defect B fix:
// PrimaryValidated=true only skips the stale freshness check, NOT the kind check.
// An item whose primary row has the wrong kind must still be excluded even if validated.
func TestMultiQuerySemanticSearchSQLValidatedWrongKindExcluded(t *testing.T) {
	t.Parallel()

	wrongKindID := uuid.New()
	rightKindID := uuid.New()

	// wrongKindID: PrimaryValidated=true, primary kind="other/v1" → must be excluded.
	// rightKindID: PrimaryValidated=false, primary kind="events/v1", vector matches → included.
	// Non-SQL candidates require a positive vector revision exactly matching the primary row.
	page := []registryepisodic.MemoryVectorSearch{
		// wrongKindID has the higher vector score but wrong primary kind.
		{MemoryID: wrongKindID, Score: 0.99, MemoryKind: "events/v1", MemoryRevision: 1, PrimaryValidated: true},
		{MemoryID: rightKindID, Score: 0.80, MemoryKind: "events/v1", MemoryRevision: 1, PrimaryValidated: false},
	}
	store := &fakeEpisodicStore{
		vectorPages: [][]registryepisodic.MemoryVectorSearch{page},
		// wrongKindID's primary row has "other/v1" — wrong kind despite PrimaryValidated=true.
		kindByID: map[uuid.UUID]string{
			wrongKindID: "other/v1",
			rightKindID: "events/v1",
		},
	}

	queries := []searchQuerySpec{{Text: "q", Purpose: "p"}}
	ctx := context.Background()
	results, err := multiQuerySemanticSearch(ctx, store, theEmbedder, nil, registryepisodic.AttributeFilter{}, queries, 5, 5, registryepisodic.ArchiveFilterInclude, "events/v1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, r := range results {
		if r.ID == wrongKindID {
			t.Error("PrimaryValidated=true must NOT bypass kind check: wrong-kind item should be excluded")
		}
	}
	found := false
	for _, r := range results {
		if r.ID == rightKindID {
			found = true
		}
	}
	if !found {
		t.Errorf("right-kind item should be included; got %d results", len(results))
	}
}

// TestMultiQuerySemanticSearchExactSelectorFilters verifies that an exact selector only
// returns rows with the matching kind.
func TestMultiQuerySemanticSearchExactSelectorFilters(t *testing.T) {
	t.Parallel()

	matchID := uuid.New()
	mismatchID := uuid.New()

	page := []registryepisodic.MemoryVectorSearch{
		{MemoryID: matchID, Score: 0.95, MemoryKind: "events/v1", PrimaryValidated: true},
		{MemoryID: mismatchID, Score: 0.90, MemoryKind: "events/v2", PrimaryValidated: true},
	}
	store := &fakeEpisodicStore{
		vectorPages: [][]registryepisodic.MemoryVectorSearch{page},
		kindByID: map[uuid.UUID]string{
			matchID:    "events/v1",
			mismatchID: "events/v2",
		},
	}

	queries := []searchQuerySpec{{Text: "q", Purpose: "p"}}
	ctx := context.Background()
	// Selector is exact "events/v1" — only matchID should appear.
	results, err := multiQuerySemanticSearch(ctx, store, theEmbedder, nil, registryepisodic.AttributeFilter{}, queries, 5, 5, registryepisodic.ArchiveFilterInclude, "events/v1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, r := range results {
		if r.ID == matchID {
			found = true
		}
		if r.ID == mismatchID {
			t.Error("mismatched kind item must not appear in exact selector results")
		}
	}
	if !found {
		t.Errorf("exact-match item must appear; got %d results", len(results))
	}
}

// TestMultiQuerySemanticSearchAllSelectorMatchesAllKinds verifies that an empty
// selector (all schemas) matches rows with any kind.
func TestMultiQuerySemanticSearchAllSelectorMatchesAllKinds(t *testing.T) {
	t.Parallel()

	id1 := uuid.New()

	page := []registryepisodic.MemoryVectorSearch{
		{MemoryID: id1, Score: 0.9, MemoryKind: "default/v1", PrimaryValidated: true},
	}
	store := &fakeEpisodicStore{
		vectorPages: [][]registryepisodic.MemoryVectorSearch{page},
		kindByID:    map[uuid.UUID]string{id1: "default/v1"},
	}

	queries := []searchQuerySpec{{Text: "q", Purpose: "p"}}
	ctx := context.Background()
	// Empty selector = all schemas.
	results, err := multiQuerySemanticSearch(ctx, store, theEmbedder, nil, registryepisodic.AttributeFilter{}, queries, 5, 5, registryepisodic.ArchiveFilterInclude, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, r := range results {
		if r.ID == id1 {
			found = true
		}
	}
	if !found {
		t.Errorf("default/v1 row must match empty (all) selector; got %d results", len(results))
	}
}

// --- Bug 1: validateAndNormalizeCallerFilter returns canonical normalized filter ---

// fakeKindStore is a minimal EpisodicStore that returns a fixed list of schema versions.
type fakeKindStore struct {
	registryepisodic.EpisodicStore
	versions []model.MemoryKindVersion
}

func (f *fakeKindStore) InReadTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}
func (f *fakeKindStore) InWriteTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}
func (f *fakeKindStore) ListMemoryKindVersions(_ context.Context, _ string) ([]model.MemoryKindVersion, error) {
	return f.versions, nil
}
func (f *fakeKindStore) GetMemoryKindVersion(_ context.Context, name string) (*model.MemoryKindVersion, error) {
	for _, v := range f.versions {
		if v.Name == name {
			cp := v
			return &cp, nil
		}
	}
	return nil, nil
}

// TestValidateAndNormalizeCallerFilterNormalizesTimestamp verifies that a timestamp filter
// value is canonicalized to nanosecond UTC form.
func TestValidateAndNormalizeCallerFilterNormalizesTimestamp(t *testing.T) {
	t.Parallel()
	store := &fakeKindStore{
		versions: []model.MemoryKindVersion{
			{Name: "events/v1", AttributeTypes: map[string]string{"created_at": "timestamp"}},
		},
	}
	callerFilter := registryepisodic.AttributeFilter{
		Conditions: []registryepisodic.AttributeFilterCondition{
			{
				Field: "created_at",
				Op:    registryepisodic.AttributeFilterOpEq,
				Values: []registryepisodic.AttributeFilterValue{
					{Raw: "2024-03-15T10:30:00Z", Text: "2024-03-15T10:30:00Z"},
				},
			},
		},
	}
	out, err := validateAndNormalizeCallerFilter(context.Background(), store, "events/v1", callerFilter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Conditions) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(out.Conditions))
	}
	v := out.Conditions[0].Values[0]
	s, ok := v.Raw.(string)
	if !ok {
		t.Fatalf("expected string Raw, got %T", v.Raw)
	}
	if s != "2024-03-15T10:30:00.000000000Z" {
		t.Errorf("canonical timestamp = %q, want 2024-03-15T10:30:00.000000000Z", s)
	}
	// RangeKind should be set to time.
	if out.Conditions[0].RangeKind != registryepisodic.AttributeFilterRangeTime {
		t.Errorf("RangeKind = %v, want AttributeFilterRangeTime", out.Conditions[0].RangeKind)
	}
}

// TestValidateAndNormalizeCallerFilterNormalizesNumber verifies RangeKind=number for number fields.
func TestValidateAndNormalizeCallerFilterNormalizesNumber(t *testing.T) {
	t.Parallel()
	store := &fakeKindStore{
		versions: []model.MemoryKindVersion{
			{Name: "events/v1", AttributeTypes: map[string]string{"score": "number"}},
		},
	}
	callerFilter := registryepisodic.AttributeFilter{
		Conditions: []registryepisodic.AttributeFilterCondition{
			{
				Field: "score",
				Op:    registryepisodic.AttributeFilterOpGte,
				Values: []registryepisodic.AttributeFilterValue{
					{Raw: float64(42), Text: "42"},
				},
			},
		},
	}
	out, err := validateAndNormalizeCallerFilter(context.Background(), store, "events/v1", callerFilter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Conditions[0].RangeKind != registryepisodic.AttributeFilterRangeNumber {
		t.Errorf("RangeKind = %v, want AttributeFilterRangeNumber", out.Conditions[0].RangeKind)
	}
}

// TestValidateAndNormalizeCallerFilterRejectsUndeclaredField verifies that a filter on
// a field not declared in the schema returns an error.
func TestValidateAndNormalizeCallerFilterRejectsUndeclaredField(t *testing.T) {
	t.Parallel()
	store := &fakeKindStore{
		versions: []model.MemoryKindVersion{
			{Name: "events/v1", AttributeTypes: map[string]string{"score": "number"}},
		},
	}
	callerFilter := registryepisodic.AttributeFilter{
		Conditions: []registryepisodic.AttributeFilterCondition{
			{
				Field:  "undeclared",
				Op:     registryepisodic.AttributeFilterOpEq,
				Values: []registryepisodic.AttributeFilterValue{{Raw: "x"}},
			},
		},
	}
	_, err := validateAndNormalizeCallerFilter(context.Background(), store, "events/v1", callerFilter)
	if err == nil {
		t.Fatal("expected error for undeclared field, got nil")
	}
}

// TestValidateAndNormalizeCallerFilterEmptyIsPassThrough verifies that an empty filter
// (nil/empty conditions) is returned unchanged.
func TestValidateAndNormalizeCallerFilterEmptyPassThrough(t *testing.T) {
	t.Parallel()
	store := &fakeKindStore{
		versions: []model.MemoryKindVersion{
			{Name: "events/v1", AttributeTypes: map[string]string{"score": "number"}},
		},
	}
	out, err := validateAndNormalizeCallerFilter(context.Background(), store, "events/v1", registryepisodic.AttributeFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Conditions) != 0 {
		t.Errorf("expected empty filter unchanged, got %d conditions", len(out.Conditions))
	}
}

// Compile-check: fakeKindStore must satisfy EpisodicStore (partial via embedding).
var _ registryepisodic.EpisodicStore = (*fakeKindStore)(nil)

// Compile-check: fake stores must satisfy the search interface subset.
var _ registryepisodic.EpisodicStore = (*fakeEpisodicStore)(nil)
