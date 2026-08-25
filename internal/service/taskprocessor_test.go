package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/log"
	"github.com/chirino/memory-service/internal/model"
	registryepisodic "github.com/chirino/memory-service/internal/registry/episodic"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
	registryvector "github.com/chirino/memory-service/internal/registry/vector"
	"github.com/google/uuid"
)

func TestTaskProcessorEmitsOneTerminalEventPerClaimedAttempt(t *testing.T) {
	tests := []struct {
		name         string
		executeErr   error
		executePanic any
		failErr      error
		deleteErr    error
		cancel       bool
		deadline     bool
		wantResult   string
		wantReason   string
		pointLogs    []string
	}{
		{name: "success", wantResult: "success"},
		{name: "retry scheduled", executeErr: errors.New("private provider response"), wantResult: "retrying", wantReason: "retry_scheduled", pointLogs: []string{"TaskProcessor: task failed"}},
		{name: "retry persistence failure", executeErr: errors.New("private provider response"), failErr: errors.New("private database error"), wantResult: "failed", wantReason: "retry_persistence_failed", pointLogs: []string{"TaskProcessor: task failed", "TaskProcessor: fail task record failed"}},
		{name: "cleanup failure", deleteErr: errors.New("private database error"), wantResult: "failed", wantReason: "task_cleanup_failed", pointLogs: []string{"TaskProcessor: delete task failed"}},
		{name: "cleanup cancellation", deleteErr: fmt.Errorf("delete interrupted: %w", context.Canceled), wantResult: "failed", wantReason: "task_cleanup_failed", pointLogs: []string{"TaskProcessor: delete task failed"}},
		{name: "cleanup deadline", deleteErr: fmt.Errorf("delete interrupted: %w", context.DeadlineExceeded), wantResult: "failed", wantReason: "task_cleanup_failed", pointLogs: []string{"TaskProcessor: delete task failed"}},
		{name: "cancellation", executeErr: context.Canceled, cancel: true, wantResult: "canceled", wantReason: "shutdown"},
		{name: "wrapped cancellation", executeErr: fmt.Errorf("vector request stopped: %w", context.Canceled), wantResult: "canceled", wantReason: "shutdown"},
		{name: "deadline", executeErr: context.DeadlineExceeded, deadline: true, wantResult: "timed_out", wantReason: "deadline"},
		{name: "panic", executePanic: "private panic", wantResult: "failed", wantReason: "panic", pointLogs: []string{"operation panic", "taskprocessor_test.go"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			log.SetOutput(&output)
			log.SetReportTimestamp(false)
			t.Cleanup(func() {
				log.SetOutput(os.Stderr)
				log.SetReportTimestamp(true)
			})

			taskID := uuid.New()
			store := &taskProcessorTestStore{
				tasks: []model.Task{{
					ID: taskID, TaskType: "vector_store_delete", RetryCount: 2,
					TaskBody: map[string]any{"conversationGroupId": uuid.NewString()},
				}},
				failErr: tt.failErr, deleteErr: tt.deleteErr,
			}
			processor := NewTaskProcessor(store, &taskProcessorTestVector{err: tt.executeErr, panicValue: tt.executePanic})
			ctx := context.Background()
			if tt.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			} else if tt.deadline {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			}
			processor.processBatch(ctx)

			text := output.String()
			if strings.Count(text, "phase=complete") != 1 {
				t.Fatalf("expected one terminal event, got:\n%s", text)
			}
			for _, expected := range []string{"job.vector_store_delete", "taskID=" + taskID.String(), "retryAttempt=3", "result=" + tt.wantResult} {
				if !strings.Contains(text, expected) {
					t.Fatalf("event missing %q:\n%s", expected, text)
				}
			}
			if tt.wantReason != "" && !strings.Contains(text, "reason="+tt.wantReason) {
				t.Fatalf("event missing reason %q:\n%s", tt.wantReason, text)
			}
			for _, pointLog := range tt.pointLogs {
				if !strings.Contains(text, pointLog) {
					t.Fatalf("missing diagnostic point log %q:\n%s", pointLog, text)
				}
			}
			for _, line := range strings.Split(text, "\n") {
				if strings.Contains(line, "job.vector_store_delete") && strings.Contains(line, "phase=complete") &&
					(strings.Contains(line, "private provider response") || strings.Contains(line, "private database error") || strings.Contains(line, "private panic")) {
					t.Fatalf("raw error leaked into canonical event:\n%s", line)
				}
			}
		})
	}
}

type taskProcessorTestStore struct {
	registrystore.MemoryStore
	tasks     []model.Task
	failErr   error
	deleteErr error
}

func (s *taskProcessorTestStore) InWriteTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *taskProcessorTestStore) ClaimReadyTasks(context.Context, int) ([]model.Task, error) {
	return s.tasks, nil
}

func (s *taskProcessorTestStore) FailTask(context.Context, uuid.UUID, string, time.Duration) error {
	return s.failErr
}

func (s *taskProcessorTestStore) DeleteTask(context.Context, uuid.UUID) error {
	return s.deleteErr
}

type taskProcessorTestVector struct {
	err        error
	panicValue any
}

func (v *taskProcessorTestVector) Search(context.Context, []float32, []uuid.UUID, int) ([]registryvector.VectorSearchResult, error) {
	return nil, nil
}
func (v *taskProcessorTestVector) Upsert(context.Context, []registryvector.UpsertRequest) error {
	return nil
}
func (v *taskProcessorTestVector) DeleteByConversationGroupID(context.Context, uuid.UUID) error {
	if v.panicValue != nil {
		panic(v.panicValue)
	}
	return v.err
}
func (v *taskProcessorTestVector) IsEnabled() bool { return true }
func (v *taskProcessorTestVector) Name() string    { return "test" }

// --- Bug 6: classifyMigrationError uses typed sentinels, not string matching ---

func TestClassifyMigrationErrorTypedSentinels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", err: nil, want: ""},
		{name: "deadline", err: context.DeadlineExceeded, want: "deadline_exceeded"},
		{name: "canceled", err: context.Canceled, want: "canceled"},
		{name: "wrapped deadline", err: fmt.Errorf("wrap: %w", context.DeadlineExceeded), want: "deadline_exceeded"},
		{name: "cas conflict sentinel", err: &migErrCASConflict{cause: fmt.Errorf("oops")}, want: "cas_conflict"},
		{name: "reschedule sentinel", err: &migErrRescheduleFailed{cause: fmt.Errorf("fail")}, want: "reschedule_failed"},
		{name: "scan sentinel", err: &migErrScanFailed{cause: fmt.Errorf("fail")}, want: "scan_failed"},
		{name: "load sentinel", err: &migErrLoadFailed{cause: fmt.Errorf("fail")}, want: "load_failed"},
		{name: "state update sentinel", err: &migErrStateUpdateFailed{cause: fmt.Errorf("fail")}, want: "state_update_failed"},
		{name: "wrapped cas sentinel", err: fmt.Errorf("wrap: %w", &migErrCASConflict{cause: fmt.Errorf("inner")}), want: "cas_conflict"},
		// Unrecognised errors should not expose raw text; they return "transient_error".
		{name: "random db error with cas text", err: fmt.Errorf("cas conflict in database"), want: "transient_error"},
		{name: "random text with reschedule", err: fmt.Errorf("reschedule failed"), want: "transient_error"},
		{name: "unknown error", err: fmt.Errorf("something unknown"), want: "transient_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyMigrationError(tt.err)
			if got != tt.want {
				t.Errorf("classifyMigrationError(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

// Bug 6: verify that raw provider text in an opaque error maps to transient_error,
// NOT to a deterministic category that would indicate internal logic ran.
func TestClassifyMigrationErrorDoesNotMatchProviderText(t *testing.T) {
	t.Parallel()
	// These strings appear in old string-match branches; they must now map to transient_error.
	rawTexts := []string{
		"cas conflict in row 123",
		"revision conflict detected",
		"reschedule task failed",
		"continuation task missing",
		"find memories failed on shard 3",
		"load migration state error",
		"load target schema version error",
		"persist state update failed",
	}
	for _, msg := range rawTexts {
		code := classifyMigrationError(fmt.Errorf("%s", msg))
		if code != "transient_error" {
			t.Errorf("raw text %q classified as %q (want transient_error — string matching is disallowed)", msg, code)
		}
	}
}

// --- Bug 2: counter error in continuation task must be fatal (return error) ---

// --- Deterministic migration batch tests ---

// txKey is the context key for the in-progress write transaction state in migTestEpisodicStore.
type txKey struct{}

// txState holds per-InWriteTx pending changes that are committed only on success.
type txState struct {
	migratedDelta  int64
	migratedIDsAdd []uuid.UUID
}

// migTestEpisodicStore is a configurable EpisodicStore stub for deterministic
// migration batch tests. It supports multiple sequential scan batches and tracks
// all store method calls for assertion.
//
// InWriteTx is transactional: staged changes (migratedCount, migratedIDs) are
// committed to the store only if the callback returns nil; they are rolled back
// on error.  This mirrors real SQL transaction semantics and catches the escaped-
// transaction bug (defect 1/2).
//
// Successfully migrated IDs are tracked in migratedIDs; FindMemoriesToMigrateByKind
// filters them out so re-scans after migration see the correct state.
type migTestEpisodicStore struct {
	registryepisodic.EpisodicStore

	// Scan batches: each call to FindMemoriesToMigrateByKind pops the front element.
	// An empty/exhausted list returns [].
	scanBatches [][]registryepisodic.MigrationCandidate

	// migratedIDs is the committed set of successfully migrated memory IDs.
	// After a successful InWriteTx that called MigrateOneMemoryKindCAS, the ID
	// is added here so subsequent FindMemoriesToMigrateByKind calls filter it out.
	migratedIDs map[uuid.UUID]struct{}

	// incrementMigratedErr is returned from UpdateMemoryKindMigrationIncrementMigrated.
	incrementMigratedErr error
	// casErr is returned from MigrateOneMemoryKindCAS.
	casErr error
	// succeedErr is returned from UpdateMemoryKindMigrationSucceeded.
	succeedErr        error
	migration         *model.MemoryKindMigration
	getMigrationCalls int
	cancelOnGetCall   int

	// Committed counters.
	migratedCount      int64
	succeededCalled    bool
	succeededTombstone int64
	retryCount         int
	stateFailedCalled  bool
	stateFailedCode    string
	createTaskCalled   bool
}

func (s *migTestEpisodicStore) InReadTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}

// InWriteTx stages changes inside fn and commits them only on success.
// A staged migratedCount increment is rolled back if fn returns an error,
// mimicking SQL transaction rollback semantics.
func (s *migTestEpisodicStore) InWriteTx(_ context.Context, fn func(context.Context) error) error {
	state := &txState{}
	txCtx := context.WithValue(context.Background(), txKey{}, state)
	err := fn(txCtx)
	if err == nil {
		// Commit: apply staged changes.
		s.migratedCount += state.migratedDelta
		if s.migratedIDs == nil {
			s.migratedIDs = make(map[uuid.UUID]struct{})
		}
		for _, id := range state.migratedIDsAdd {
			s.migratedIDs[id] = struct{}{}
		}
	}
	// On error: staged changes are discarded (rollback).
	return err
}

func (s *migTestEpisodicStore) GetMemoryKindMigration(_ context.Context, _ uuid.UUID) (*model.MemoryKindMigration, error) {
	s.getMigrationCalls++
	if s.migration == nil {
		s.migration = &model.MemoryKindMigration{
			ID: uuid.New(), State: model.MigrationStateRunning, Source: "profile/v1", Target: "default/v1",
		}
	}
	if s.cancelOnGetCall > 0 && s.getMigrationCalls >= s.cancelOnGetCall {
		s.migration.CancelRequested = true
		s.migration.State = model.MigrationStateCanceling
	}
	copy := *s.migration
	return &copy, nil
}
func (s *migTestEpisodicStore) GetMemoryKindVersion(_ context.Context, _ string) (*model.MemoryKindVersion, error) {
	return &model.MemoryKindVersion{Name: "default/v1"}, nil
}

// FindMemoriesToMigrateByKind pops the next scan batch and filters out any
// successfully committed migrated IDs so re-scans reflect true DB state.
func (s *migTestEpisodicStore) FindMemoriesToMigrateByKind(_ context.Context, _ string, _ []string, _ time.Time, _ uuid.UUID, _ int) ([]registryepisodic.MigrationCandidate, error) {
	if len(s.scanBatches) == 0 {
		return nil, nil
	}
	raw := s.scanBatches[0]
	s.scanBatches = s.scanBatches[1:]
	if len(s.migratedIDs) == 0 {
		return raw, nil
	}
	// Filter out committed migrated IDs.
	out := raw[:0]
	for _, c := range raw {
		if _, done := s.migratedIDs[c.ID]; !done {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *migTestEpisodicStore) CreateMemoryKindMigrationTask(_ context.Context, _ map[string]interface{}) error {
	s.createTaskCalled = true
	return nil
}

// MigrateOneMemoryKindCAS stages the ID for commit if successful.
func (s *migTestEpisodicStore) MigrateOneMemoryKindCAS(ctx context.Context, id uuid.UUID, _ string, _ int64, _ map[string]interface{}, _ string) error {
	if s.casErr != nil {
		return s.casErr
	}
	if state, ok := ctx.Value(txKey{}).(*txState); ok && state != nil {
		state.migratedIDsAdd = append(state.migratedIDsAdd, id)
	}
	return nil
}

// UpdateMemoryKindMigrationIncrementMigrated stages a +1 delta for commit.
// If incrementMigratedErr is set, returns the error (staged delta is discarded
// by InWriteTx rollback, leaving migratedCount unchanged).
func (s *migTestEpisodicStore) UpdateMemoryKindMigrationIncrementMigrated(ctx context.Context, _ uuid.UUID) error {
	if s.incrementMigratedErr != nil {
		return s.incrementMigratedErr
	}
	if state, ok := ctx.Value(txKey{}).(*txState); ok && state != nil {
		state.migratedDelta++
	}
	return nil
}
func (s *migTestEpisodicStore) UpdateMemoryKindMigrationCounters(_ context.Context, _ uuid.UUID, _, _, _ int64) error {
	return nil
}
func (s *migTestEpisodicStore) UpdateMemoryKindMigrationSucceeded(_ context.Context, _ uuid.UUID, _ time.Time, absoluteSkippedTombstoneCount int64) error {
	if s.succeedErr != nil {
		return s.succeedErr
	}
	s.succeededCalled = true
	s.succeededTombstone = absoluteSkippedTombstoneCount
	return nil
}
func (s *migTestEpisodicStore) UpdateMemoryKindMigrationState(_ context.Context, _ uuid.UUID, state string, _, _ *time.Time) error {
	if s.migration != nil {
		s.migration.State = state
		if state == model.MigrationStateCanceled {
			s.migration.CancelRequested = true
		}
	}
	return nil
}
func (s *migTestEpisodicStore) UpdateMemoryKindMigrationStateFailed(_ context.Context, _ uuid.UUID, code string, _ time.Time) error {
	s.stateFailedCalled = true
	s.stateFailedCode = code
	return nil
}
func (s *migTestEpisodicStore) UpdateMemoryKindMigrationRetry(_ context.Context, _ uuid.UUID, _ string) error {
	s.retryCount++
	return nil
}

// migTestTaskStore combines ClaimReadyTasks, CreateTask, DeleteTask for migration tests.
type migTestTaskStore struct {
	registrystore.MemoryStore
	tasks        []model.Task
	createCalled bool
	deleteCalled bool
	failCalled   bool
	createErr    error
}

func (s *migTestTaskStore) InWriteTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}
func (s *migTestTaskStore) ClaimReadyTasks(context.Context, int) ([]model.Task, error) {
	return s.tasks, nil
}
func (s *migTestTaskStore) CreateTask(_ context.Context, _ string, _ map[string]any) error {
	s.createCalled = true
	return s.createErr
}
func (s *migTestTaskStore) DeleteTask(_ context.Context, _ uuid.UUID) error {
	s.deleteCalled = true
	return nil
}
func (s *migTestTaskStore) FailTask(_ context.Context, _ uuid.UUID, _ string, _ time.Duration) error {
	s.failCalled = true
	return nil
}

func makeMigTask(t *testing.T, migID uuid.UUID) []model.Task {
	t.Helper()
	return []model.Task{{
		ID:       uuid.New(),
		TaskType: "memory_kind_migration",
		TaskBody: map[string]any{"migration_id": migID.String()},
	}}
}

// liveCandidate returns a MigrationCandidate representing a live (non-tombstone) row.
// Namespace is set to a valid single-segment encoding ("test") so DecodeNamespace succeeds.
func liveCandidate() registryepisodic.MigrationCandidate {
	return registryepisodic.MigrationCandidate{
		ID:             uuid.New(),
		Namespace:      "test",
		ValueEncrypted: []byte(`{}`),
		MemoryKind:     "profile/v1",
		Revision:       1,
	}
}

// tombstoneCandidate returns a MigrationCandidate representing a tombstone (nil value).
func tombstoneCandidate() registryepisodic.MigrationCandidate {
	return registryepisodic.MigrationCandidate{
		ID:             uuid.New(),
		ValueEncrypted: nil,
		MemoryKind:     "profile/v1",
		Revision:       1,
	}
}

func TestMigrationCancellationStopsBeforeNextCandidate(t *testing.T) {
	migID := uuid.New()
	store := &migTestEpisodicStore{
		migration:       &model.MemoryKindMigration{ID: migID, State: model.MigrationStateRunning, Source: "profile/v1", Target: "default/v1"},
		cancelOnGetCall: 3, // initial load, first-row check, then cancellation before row two
		scanBatches:     [][]registryepisodic.MigrationCandidate{{liveCandidate(), liveCandidate()}},
	}
	tasks := &migTestTaskStore{tasks: makeMigTask(t, migID)}
	processor := NewTaskProcessor(tasks, nil)
	processor.SetEpisodicStore(store, nil)

	processor.processBatch(context.Background())
	if store.migratedCount != 1 {
		t.Fatalf("expected cancellation to stop after the current row, migrated %d", store.migratedCount)
	}
	if store.migration.State != model.MigrationStateCanceled {
		t.Fatalf("expected canceled state, got %s", store.migration.State)
	}
	if store.createTaskCalled || !tasks.deleteCalled {
		t.Fatalf("canceled batch should delete its task without a continuation")
	}
}

func TestMigrationCancellationWinsDuringFinalSweep(t *testing.T) {
	migID := uuid.New()
	store := &migTestEpisodicStore{
		migration:       &model.MemoryKindMigration{ID: migID, State: model.MigrationStateRunning, Source: "profile/v1", Target: "default/v1"},
		cancelOnGetCall: 2, // cancellation becomes visible inside finalization
		scanBatches:     [][]registryepisodic.MigrationCandidate{{}},
	}
	tasks := &migTestTaskStore{tasks: makeMigTask(t, migID)}
	processor := NewTaskProcessor(tasks, nil)
	processor.SetEpisodicStore(store, nil)

	processor.processBatch(context.Background())
	if store.succeededCalled {
		t.Fatal("finalization must not overwrite a cancellation with succeeded")
	}
	if store.migration.State != model.MigrationStateCanceled {
		t.Fatalf("expected canceled state, got %s", store.migration.State)
	}
}

// TestMigration60TombstonesThenOneLive verifies defect 4 fix:
// a batch of 60 tombstones followed by one live row across multiple scan batches
// produces skipped_tombstone_count=60 and migrated_count=1 exactly.
//
// Scan sequence (each element is one FindMemoriesToMigrateByKind call):
//
//	Batch 0: 50 tombstones      → continuation scheduled (50 rows, cursor advances)
//	Batch 1: 10 tombs + 1 live  → live row migrated (CAS+increment); continuation scheduled
//	Batch 2: []                 → cursor exhausted; triggers verification sweep
//	Batch 3: 60 tombstones      → sweep page 1; absoluteTombstoneCount=60
//	Batch 4: {live} (filtered)  → live.ID is in migratedIDs (committed after task 2),
//	                              so the fake filters it out, returning []; sweep ends
//	                              → hasReplayable=false → UpdateMemoryKindMigrationSucceeded(60)
//
// Note on the migratedIDs filter: the migTestEpisodicStore fake tracks successfully
// committed migration CAS operations in migratedIDs. When the sweep calls
// FindMemoriesToMigrateByKind for batch 4, live.ID is already committed → filtered to []
// → the sweep observes an empty page and correctly concludes no replayable rows remain.
// This means Task 3 succeeds directly without scheduling a restart (unlike a real store
// where the source row would still exist in the DB until its kind/revision changes, but
// the fake's filtering faithfully simulates "source row no longer matches source kind").
func TestMigration60TombstonesThenOneLive(t *testing.T) {
	t.Parallel()

	migID := uuid.New()

	// Build 60 tombstones.
	tombs := make([]registryepisodic.MigrationCandidate, 60)
	for i := range tombs {
		tombs[i] = tombstoneCandidate()
	}
	live := liveCandidate()

	episodicStore := &migTestEpisodicStore{
		// Scan batches consumed across ALL task invocations in sequence.
		// Each FindMemoriesToMigrateByKind call pops the next batch from this queue.
		//
		// Task 1 (50 tombs → continuation):
		//   batch 0: 50 tombstones (batch processing)
		// Task 2 (10 tombs + live → continuation):
		//   batch 1: tombs[50:] + live (batch processing; live is migrated; live.ID→migratedIDs)
		// Task 3 (cursor empty → sweep → succeed directly):
		//   batch 2: [] (main scan — cursor exhausted, triggers sweep)
		//   batch 3: tombs (sweep page 1: 60 tombstones; absoluteTombstoneCount=60)
		//   batch 4: {live} → filtered to [] by migratedIDs → sweep ends; hasReplayable=false
		//   → UpdateMemoryKindMigrationSucceeded(60)
		scanBatches: [][]registryepisodic.MigrationCandidate{
			tombs[:50],               // task 1: batch scan (50 tombstones)
			append(tombs[50:], live), // task 2: batch scan (10 tombs + live)
			{},                       // task 3: main scan empty → sweep
			tombs,                    // task 3: sweep page 1 (60 tombstones)
			{live},                   // task 3: sweep page 2 ({live} filtered→[] by migratedIDs)
		},
	}

	taskStore := &migTestTaskStore{}
	p := NewTaskProcessor(taskStore, nil)
	p.SetEpisodicStore(episodicStore, nil)

	// Helper to run one processBatch with the given migID task.
	runTask := func() {
		taskStore.tasks = makeMigTask(t, migID)
		taskStore.createCalled = false
		episodicStore.createTaskCalled = false
		taskStore.deleteCalled = false
		taskStore.failCalled = false
		p.processBatch(context.Background())
	}

	// Task 1: 50 tombstones → continuation scheduled (CreateTask) + task deleted.
	runTask()
	if !taskStore.deleteCalled {
		t.Error("task 1: expected task deletion after continuation scheduled")
	}
	if !episodicStore.createTaskCalled {
		t.Error("task 1: expected episodic migration task for continuation")
	}

	// Task 2: 10 tombstones + 1 live → live migrated; continuation scheduled; task deleted.
	runTask()
	if !taskStore.deleteCalled {
		t.Error("task 2: expected task deletion after continuation")
	}
	if episodicStore.migratedCount != 1 {
		t.Errorf("task 2: expected migratedCount=1 after live row, got %d", episodicStore.migratedCount)
	}

	// Task 3: cursor exhausted → verification sweep:
	//   - Sweep page 1: 60 tombstones → absoluteTombstoneCount=60
	//   - Sweep page 2: {live} filtered to [] (live.ID in migratedIDs) → hasReplayable=false
	//   → UpdateMemoryKindMigrationSucceeded(60); task deleted; NO restart.
	runTask()
	if episodicStore.createTaskCalled {
		t.Error("task 3: must NOT create a restart task — live row already migrated (filtered by migratedIDs)")
	}
	if !taskStore.deleteCalled {
		t.Error("task 3: expected task deletion after migration succeeded")
	}
	if !episodicStore.succeededCalled {
		t.Error("task 3: expected UpdateMemoryKindMigrationSucceeded to be called")
	}
	if episodicStore.succeededTombstone != 60 {
		t.Errorf("task 3: expected absoluteSkippedTombstoneCount=60, got %d", episodicStore.succeededTombstone)
	}
	if episodicStore.migratedCount != 1 {
		t.Errorf("task 3: expected migratedCount=1 (live row migrated exactly once), got %d", episodicStore.migratedCount)
	}
}

// TestMigrationCASConflictStopsBatchAndRetries verifies defect 4 fix:
// a CAS conflict on a live row stops the batch immediately, returns a typed error,
// and the task retries (no delete, no continuation). On retry the conflicting row
// (live1) re-appears in the scan together with any subsequent rows (live2), so both
// are migrated exactly once and the migration ultimately succeeds.
//
// Scan batches consumed across all task invocations:
//
//	batch 0: [tombstone, live1, live2]  — task 1: CAS on live1 fails → retry
//	                                       (live2 was never reached; tombstone cursor
//	                                        advanced but live1 stopped progress; no
//	                                        after_id is stored — fresh task re-scans
//	                                        from the beginning)
//	batch 1: [live1, live2]             — task 2 (retry): live1 succeeds, live2 succeeds;
//	                                       continuation scheduled
//	batch 2: []                         — task 3 (continuation): cursor exhausted → sweep
//	batch 3: []                         — sweep: nothing → succeeded
func TestMigrationCASConflictStopsBatchAndRetries(t *testing.T) {
	t.Parallel()

	migID := uuid.New()
	live1 := liveCandidate()
	live2 := liveCandidate()

	taskStore := &migTestTaskStore{}
	p := NewTaskProcessor(taskStore, nil)

	customEpisodic := &migTestEpisodicStore{
		scanBatches: [][]registryepisodic.MigrationCandidate{
			{tombstoneCandidate(), live1, live2}, // task 1: CAS conflict on live1 → retry
			{live1, live2},                       // task 2 (retry): both migrated → continuation
			{},                                   // task 3: cursor exhausted → sweep
			{},                                   // sweep: nothing → succeeded
		},
	}

	// casFirstErrorEpisodicStore wraps customEpisodic and errors on the first CAS only.
	p.SetEpisodicStore(&casFirstErrorEpisodicStore{
		migTestEpisodicStore: customEpisodic,
	}, nil)

	runTask := func() {
		taskStore.tasks = makeMigTask(t, migID)
		taskStore.createCalled = false
		customEpisodic.createTaskCalled = false
		taskStore.deleteCalled = false
		taskStore.failCalled = false
		p.processBatch(context.Background())
	}

	// Task 1: tombstone + live1 (CAS conflict) + live2 → batch stops at live1.
	// No after_id progress because the conflicting row was not processed.
	// Task retries (FailTask called), NOT deleted, NO continuation.
	runTask()
	if taskStore.deleteCalled {
		t.Error("task 1: task should NOT be deleted on CAS conflict — must retry")
	}
	if customEpisodic.createTaskCalled {
		t.Error("task 1: continuation must NOT be created on CAS conflict")
	}
	if !taskStore.failCalled {
		t.Error("task 1: FailTask must be called so the task retries")
	}

	// Task 2 (retry): live1 now succeeds (first CAS error already consumed), then live2.
	// Both migrated; continuation scheduled.
	runTask()
	if !customEpisodic.createTaskCalled {
		t.Error("task 2: expected continuation after retry processes live1 + live2")
	}
	if !taskStore.deleteCalled {
		t.Error("task 2: expected task deletion after successful continuation scheduled")
	}
	if customEpisodic.migratedCount != 2 {
		t.Errorf("task 2: expected migratedCount=2 (live1+live2 each migrated once), got %d", customEpisodic.migratedCount)
	}

	// Task 3: cursor exhausted → sweep → succeeded.
	runTask()
	if !customEpisodic.succeededCalled {
		t.Error("task 3: expected succeeded after retry + sweep completes")
	}
}

// casFirstErrorEpisodicStore wraps migTestEpisodicStore and returns
// ErrMemoryRevisionConflict on the first call to MigrateOneMemoryKindCAS.
type casFirstErrorEpisodicStore struct {
	*migTestEpisodicStore
	firstCAS bool
}

func (s *casFirstErrorEpisodicStore) MigrateOneMemoryKindCAS(ctx context.Context, id uuid.UUID, expectedKind string, expectedRevision int64, newAttributes map[string]interface{}, newSchema string) error {
	if !s.firstCAS {
		s.firstCAS = true
		return registryepisodic.ErrMemoryRevisionConflict
	}
	return s.migTestEpisodicStore.MigrateOneMemoryKindCAS(ctx, id, expectedKind, expectedRevision, newAttributes, newSchema)
}

// TestMigrationIncrementMigratedErrorRollsBackContinuation verifies defect 2 fix:
// when UpdateMemoryKindMigrationIncrementMigrated returns an error, the task must
// retry (FailTask called, NOT deleted) and CreateTask must NOT be called.
func TestMigrationIncrementMigratedErrorRollsBackContinuation(t *testing.T) {
	t.Parallel()

	migID := uuid.New()

	episodicStore := &migTestEpisodicStore{
		// One live row; IncrementMigrated fails.
		scanBatches: [][]registryepisodic.MigrationCandidate{
			{liveCandidate()},
		},
		incrementMigratedErr: fmt.Errorf("db locked: increment failed"),
	}

	taskStore := &migTestTaskStore{
		tasks: makeMigTask(t, migID),
	}
	p := NewTaskProcessor(taskStore, nil)
	p.SetEpisodicStore(episodicStore, nil)

	p.processBatch(context.Background())

	if taskStore.deleteCalled {
		t.Error("task should NOT be deleted when IncrementMigrated fails — must retry")
	}
	if episodicStore.createTaskCalled {
		t.Error("continuation must NOT be created when IncrementMigrated fails")
	}
	if !taskStore.failCalled {
		t.Error("FailTask must be called so the task retries")
	}
}

// TestMigrationFinalizationIdempotentAbsoluteTombstoneCount verifies defect 4 fix:
// when the finalization sweep is retried (e.g. UpdateMemoryKindMigrationSucceeded failed
// on first attempt), the absolute tombstone count from re-sweep equals the original count.
func TestMigrationFinalizationIdempotentAbsoluteTombstoneCount(t *testing.T) {
	t.Parallel()

	migID := uuid.New()

	tombs := make([]registryepisodic.MigrationCandidate, 7)
	for i := range tombs {
		tombs[i] = tombstoneCandidate()
	}

	// First finalization attempt: UpdateMemoryKindMigrationSucceeded fails.
	// Second finalization attempt: succeeds.
	succeedCallCount := 0

	episodicStore := &migTestEpisodicStore{
		// Main scan: 7 tombstones then empty → triggers first sweep.
		// Sweep 1a: 7 tombstones (absolute=7); Sweep 1b: [] → all done → first Succeeded call (fails).
		// Second task invocation: immediate empty main scan → Sweep 2a: 7 tombstones; 2b: [] → second Succeeded call (succeeds).
		scanBatches: [][]registryepisodic.MigrationCandidate{
			tombs, // task 1: batch scan (7 tombstones → continuation+cursor advance)
			{},    // task 1 (resumed): cursor exhausted → sweep starts
			tombs, // sweep 1a: 7 tombstones
			{},    // sweep 1b: empty → all done → Succeeded(7) [but fails]
			{},    // task 2 (retry): immediate empty → sweep 2 starts
			tombs, // sweep 2a: 7 tombstones
			{},    // sweep 2b: empty → all done → Succeeded(7) [succeeds]
		},
	}

	// First Succeeded call fails; second succeeds.
	episodicStore.succeedErr = fmt.Errorf("persist failed: db unavailable")

	taskStore := &migTestTaskStore{}
	p := NewTaskProcessor(taskStore, nil)
	p.SetEpisodicStore(episodicStore, nil)

	runTask := func() {
		taskStore.tasks = makeMigTask(t, migID)
		taskStore.createCalled = false
		taskStore.deleteCalled = false
		taskStore.failCalled = false
		p.processBatch(context.Background())
	}

	// Task 1a: 7 tombstones → continuation scheduled.
	runTask()
	if !taskStore.deleteCalled {
		t.Error("task 1a: expected deletion after continuation scheduled")
	}

	// Task 1b: cursor empty → sweep → Succeeded fails → task retries (failCalled).
	runTask()
	if taskStore.deleteCalled {
		t.Error("task 1b: task must NOT be deleted when Succeeded fails")
	}
	if !taskStore.failCalled {
		t.Error("task 1b: FailTask must be called when Succeeded fails")
	}
	if episodicStore.succeededCalled {
		t.Error("task 1b: succeededCalled must be false (the write failed)")
	}
	succeedCallCount++

	// Now allow Succeeded to succeed.
	episodicStore.succeedErr = nil

	// Task 2 (retry): immediate empty scan → re-sweep → Succeeded(7) succeeds.
	runTask()
	if !episodicStore.succeededCalled {
		t.Error("task 2: expected succeededCalled=true on retry")
	}
	if episodicStore.succeededTombstone != 7 {
		t.Errorf("task 2: expected absoluteSkippedTombstoneCount=7, got %d", episodicStore.succeededTombstone)
	}
	_ = succeedCallCount
}

// --- Old Bug 2 stub (retained for testTaskStoreWithCreateTask) ---

// testTaskStoreWithCreateTask tracks whether CreateTask was called.
type testTaskStoreWithCreateTask struct {
	registrystore.MemoryStore
	tasks        []model.Task
	createCalled bool
	failErr      error
	deleteErr    error
	deleteCalled bool
}

func (s *testTaskStoreWithCreateTask) InWriteTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}
func (s *testTaskStoreWithCreateTask) ClaimReadyTasks(context.Context, int) ([]model.Task, error) {
	return s.tasks, nil
}
func (s *testTaskStoreWithCreateTask) CreateTask(_ context.Context, _ string, _ map[string]any) error {
	s.createCalled = true
	return nil
}
func (s *testTaskStoreWithCreateTask) DeleteTask(_ context.Context, _ uuid.UUID) error {
	s.deleteCalled = true
	return s.deleteErr
}
func (s *testTaskStoreWithCreateTask) FailTask(_ context.Context, _ uuid.UUID, _ string, _ time.Duration) error {
	return s.failErr
}

// TestBug2IncrementMigratedErrorIsFatalRollsBackContinuation verifies that when
// UpdateMemoryKindMigrationIncrementMigrated returns an error during a live-row CAS,
// the task is NOT deleted (it retries) and the continuation CreateTask is NOT called.
// This is the replacement for the old counter-based Bug 2 test which tested
// UpdateMemoryKindMigrationCounters; that method is no longer called in batch processing.
func TestBug2IncrementMigratedErrorIsFatalRollsBackContinuation(t *testing.T) {
	t.Parallel()

	migID := uuid.New()
	taskID := uuid.New()

	episodicStore := &migTestEpisodicStore{
		// One live row; IncrementMigrated fails so the task must retry.
		scanBatches: [][]registryepisodic.MigrationCandidate{
			{liveCandidate()},
		},
		incrementMigratedErr: fmt.Errorf("counter update failed: db locked"),
	}

	taskStore := &testTaskStoreWithCreateTask{
		tasks: []model.Task{{
			ID:       taskID,
			TaskType: "memory_kind_migration",
			TaskBody: map[string]any{"migration_id": migID.String()},
		}},
	}
	p := NewTaskProcessor(taskStore, nil)
	p.SetEpisodicStore(episodicStore, nil)

	ctx := context.Background()
	p.processBatch(ctx)

	// The task should NOT have been deleted (it should retry).
	if taskStore.deleteCalled {
		t.Error("task was deleted after increment error — it should retry, not be deleted")
	}
	// CreateTask (continuation) must NOT have been called.
	if taskStore.createCalled {
		t.Error("continuation task was created despite increment error — counter and continuation must be atomic")
	}
}
