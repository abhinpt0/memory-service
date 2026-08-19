package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	registryembed "github.com/chirino/memory-service/internal/registry/embed"
	registryepisodic "github.com/chirino/memory-service/internal/registry/episodic"
	"github.com/chirino/memory-service/internal/txscope"
	"github.com/google/uuid"
)

type fakeEpisodicStore struct {
	registryepisodic.EpisodicStore

	pending    []registryepisodic.PendingMemory
	pendingErr error

	upserts          [][]registryepisodic.MemoryVectorUpsert
	deletedVectorIDs []uuid.UUID
	indexedAtByID    map[uuid.UUID]time.Time
	casErrors        []error
	operations       []string
}

func (f *fakeEpisodicStore) InReadTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(txscope.WithIntent(ctx, txscope.IntentRead))
}

func (f *fakeEpisodicStore) InWriteTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(txscope.WithIntent(ctx, txscope.IntentWrite))
}

func (f *fakeEpisodicStore) FindMemoriesPendingIndexing(_ context.Context, _ int) ([]registryepisodic.PendingMemory, error) {
	if f.pendingErr != nil {
		return nil, f.pendingErr
	}
	return f.pending, nil
}

func (f *fakeEpisodicStore) UpsertMemoryVectors(_ context.Context, items []registryepisodic.MemoryVectorUpsert) error {
	f.operations = append(f.operations, "upsert")
	cp := make([]registryepisodic.MemoryVectorUpsert, len(items))
	copy(cp, items)
	f.upserts = append(f.upserts, cp)
	return nil
}

func (f *fakeEpisodicStore) DeleteMemoryVectors(_ context.Context, memoryID uuid.UUID) error {
	f.operations = append(f.operations, "delete")
	f.deletedVectorIDs = append(f.deletedVectorIDs, memoryID)
	return nil
}

func (f *fakeEpisodicStore) SetMemoryIndexedAt(_ context.Context, memoryID uuid.UUID, indexedAt time.Time) error {
	if f.indexedAtByID == nil {
		f.indexedAtByID = make(map[uuid.UUID]time.Time)
	}
	f.indexedAtByID[memoryID] = indexedAt
	return nil
}

func (f *fakeEpisodicStore) SetMemoryKindIndexedAtCAS(_ context.Context, memoryID uuid.UUID, _ string, _ int64, indexedAt time.Time) error {
	f.operations = append(f.operations, "cas")
	if len(f.casErrors) > 0 {
		err := f.casErrors[0]
		f.casErrors = f.casErrors[1:]
		if err != nil {
			return err
		}
	}
	if f.indexedAtByID == nil {
		f.indexedAtByID = make(map[uuid.UUID]time.Time)
	}
	f.indexedAtByID[memoryID] = indexedAt
	return nil
}

func TestEpisodicIndexer_LifecycleChangeBeforeCASLeavesRowPending(t *testing.T) {
	memoryID := uuid.New()
	store := &fakeEpisodicStore{
		pending: []registryepisodic.PendingMemory{{
			ID: memoryID, Namespace: "user\x1ealice", MemoryKind: "default/v1", Revision: 1,
			IndexedContent: map[string]string{"text": "stale payload"},
		}},
		casErrors: []error{registryepisodic.ErrMemoryRevisionConflict},
	}
	indexer := NewEpisodicIndexer(store, &fakeEmbedder{embeddings: [][]float32{{1, 0}}}, time.Second, 10)
	if _, err := indexer.Trigger(context.Background()); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if _, marked := store.indexedAtByID[memoryID]; marked {
		t.Fatal("lifecycle-conflicted row must remain pending for reconciliation")
	}
	if len(store.upserts) != 1 {
		t.Fatalf("expected stale upsert attempt before CAS conflict, got %d", len(store.upserts))
	}

	// A superseding lifecycle transition clears indexed_at and increments revision.
	// The next pass must delete the stale vector before marking the row indexed.
	archivedAt := time.Now().UTC()
	supersededReason := int32(0)
	store.pending[0].ArchivedAt = &archivedAt
	store.pending[0].DeletedReason = &supersededReason
	store.pending[0].Revision = 2
	if _, err := indexer.Trigger(context.Background()); err != nil {
		t.Fatalf("second Trigger: %v", err)
	}
	if len(store.deletedVectorIDs) != 2 || store.deletedVectorIDs[1] != memoryID {
		t.Fatalf("next pass did not delete stale lifecycle vector: %#v", store.deletedVectorIDs)
	}
	if _, marked := store.indexedAtByID[memoryID]; !marked {
		t.Fatal("reconciled lifecycle revision should be marked indexed")
	}
}

func (f *fakeEpisodicStore) FindMemoriesToMigrateByKind(_ context.Context, _ string, _ []string, _ time.Time, _ uuid.UUID, _ int) ([]registryepisodic.MigrationCandidate, error) {
	return nil, nil
}

func (f *fakeEpisodicStore) MigrateOneMemoryKindCAS(_ context.Context, _ uuid.UUID, _ string, _ int64, _ map[string]interface{}, _ string) error {
	return nil
}

type fakeEmbedder struct {
	registryembed.Embedder

	embeddings [][]float32
	calls      [][]string
	err        error
}

func (f *fakeEmbedder) EmbedTexts(_ context.Context, texts []string) ([][]float32, error) {
	cp := make([]string, len(texts))
	copy(cp, texts)
	f.calls = append(f.calls, cp)
	if f.err != nil {
		return nil, f.err
	}
	return f.embeddings, nil
}

func TestEpisodicIndexer_EmbedsIndexedContentOnly(t *testing.T) {
	memoryID := uuid.New()
	store := &fakeEpisodicStore{
		pending: []registryepisodic.PendingMemory{
			{
				ID:               memoryID,
				Namespace:        "user\u001falice\u001fnotes",
				PolicyAttributes: map[string]interface{}{"namespace": "user", "sub": "alice"},
				IndexedContent: map[string]string{
					"title":   "safe title",
					"summary": "safe summary",
					"blank":   "",
				},
			},
		},
	}
	embedder := &fakeEmbedder{
		embeddings: [][]float32{
			{1, 2},
			{3, 4},
		},
	}
	indexer := NewEpisodicIndexer(store, embedder, time.Second, 10)

	stats, err := indexer.Trigger(context.Background())
	if err != nil {
		t.Fatalf("Trigger returned unexpected error: %v", err)
	}
	if stats.Pending != 1 || stats.Processed != 1 || stats.Embedded != 2 || stats.VectorUpserts != 2 || stats.SkippedNoEmbedding != 0 || stats.Failures != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	if len(embedder.calls) != 1 {
		t.Fatalf("expected one embed call, got %d", len(embedder.calls))
	}
	if !reflect.DeepEqual(embedder.calls[0], []string{"safe summary", "safe title"}) {
		t.Fatalf("unexpected embed payload: %#v", embedder.calls[0])
	}

	if len(store.upserts) != 1 {
		t.Fatalf("expected one upsert batch, got %d", len(store.upserts))
	}
	if len(store.upserts[0]) != 2 {
		t.Fatalf("expected two vector upserts, got %d", len(store.upserts[0]))
	}
	if len(store.deletedVectorIDs) != 1 || store.deletedVectorIDs[0] != memoryID {
		t.Fatalf("expected complete vector-set replacement before upsert, got deletes %#v", store.deletedVectorIDs)
	}
	if store.upserts[0][0].MemoryID != memoryID || store.upserts[0][0].FieldName != "summary" || !reflect.DeepEqual(store.upserts[0][0].Embedding, []float32{1, 2}) {
		t.Fatalf("unexpected first upsert: %#v", store.upserts[0][0])
	}
	if store.upserts[0][1].MemoryID != memoryID || store.upserts[0][1].FieldName != "title" || !reflect.DeepEqual(store.upserts[0][1].Embedding, []float32{3, 4}) {
		t.Fatalf("unexpected second upsert: %#v", store.upserts[0][1])
	}

	if _, ok := store.indexedAtByID[memoryID]; !ok {
		t.Fatalf("memory %s was not marked indexed", memoryID)
	}
}

func TestEpisodicIndexer_SkipsWhenIndexedContentMissing(t *testing.T) {
	memoryID := uuid.New()
	store := &fakeEpisodicStore{
		pending: []registryepisodic.PendingMemory{
			{
				ID:             memoryID,
				Namespace:      "user\u001falice",
				IndexedContent: map[string]string{},
			},
		},
	}
	embedder := &fakeEmbedder{}
	indexer := NewEpisodicIndexer(store, embedder, time.Second, 10)

	stats, err := indexer.Trigger(context.Background())
	if err != nil {
		t.Fatalf("Trigger returned unexpected error: %v", err)
	}
	if stats.SkippedNoEmbedding != 1 || stats.VectorUpserts != 0 || stats.Embedded != 0 || stats.Failures != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if len(embedder.calls) != 0 {
		t.Fatalf("expected no embed calls, got %d", len(embedder.calls))
	}
	if !reflect.DeepEqual(store.operations, []string{"delete", "cas"}) {
		t.Fatalf("empty replacement must delete stale vectors before CAS: %#v", store.operations)
	}
	if _, ok := store.indexedAtByID[memoryID]; !ok {
		t.Fatalf("memory %s was not marked indexed", memoryID)
	}
}

func TestEpisodicIndexer_AllEmptyContentDeletesStaleVectors(t *testing.T) {
	memoryID := uuid.New()
	store := &fakeEpisodicStore{pending: []registryepisodic.PendingMemory{{
		ID: memoryID, MemoryKind: "default/v1", Revision: 2,
		IndexedContent: map[string]string{"title": "", "summary": ""},
	}}}
	indexer := NewEpisodicIndexer(store, &fakeEmbedder{}, time.Second, 10)

	stats, err := indexer.Trigger(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Failures != 0 || len(store.upserts) != 0 {
		t.Fatalf("unexpected result: stats=%+v upserts=%v", stats, store.upserts)
	}
	if !reflect.DeepEqual(store.operations, []string{"delete", "cas"}) {
		t.Fatalf("all-empty replacement must delete stale vectors before CAS: %#v", store.operations)
	}
}

func TestEpisodicIndexer_EmbeddingFailurePreservesLastGoodVectors(t *testing.T) {
	memoryID := uuid.New()
	store := &fakeEpisodicStore{pending: []registryepisodic.PendingMemory{{
		ID: memoryID, MemoryKind: "default/v1", Revision: 2,
		IndexedContent: map[string]string{"title": "new content"},
	}}}
	indexer := NewEpisodicIndexer(store, &fakeEmbedder{err: errors.New("embed unavailable")}, time.Second, 10)

	stats, err := indexer.Trigger(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Failures != 1 || len(store.operations) != 0 {
		t.Fatalf("failed embedding must leave prior vectors and indexed_at untouched: stats=%+v ops=%v", stats, store.operations)
	}
}

func TestEpisodicIndexer_DeletesVectorsForDeletedMemory(t *testing.T) {
	memoryID := uuid.New()
	archivedAt := time.Now().Add(-time.Minute)
	store := &fakeEpisodicStore{
		pending: []registryepisodic.PendingMemory{
			{
				ID:             memoryID,
				ArchivedAt:     &archivedAt,
				Namespace:      "user\u001falice",
				IndexedContent: map[string]string{"title": "should not embed"},
			},
		},
	}
	embedder := &fakeEmbedder{}
	indexer := NewEpisodicIndexer(store, embedder, time.Second, 10)

	stats, err := indexer.Trigger(context.Background())
	if err != nil {
		t.Fatalf("Trigger returned unexpected error: %v", err)
	}
	if stats.VectorDeletes != 1 || stats.VectorUpserts != 0 || stats.SkippedNoEmbedding != 0 || stats.Failures != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if len(store.deletedVectorIDs) != 1 || store.deletedVectorIDs[0] != memoryID {
		t.Fatalf("unexpected deleted vector IDs: %#v", store.deletedVectorIDs)
	}
	if len(embedder.calls) != 0 {
		t.Fatalf("expected no embed calls for deleted memory, got %d", len(embedder.calls))
	}
	if _, ok := store.indexedAtByID[memoryID]; !ok {
		t.Fatalf("memory %s was not marked indexed", memoryID)
	}
}
