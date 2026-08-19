package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"github.com/chirino/memory-service/internal/dataencryption"
	"github.com/chirino/memory-service/internal/episodic"
	"github.com/chirino/memory-service/internal/model"
	"github.com/chirino/memory-service/internal/operationevent"
	registryepisodic "github.com/chirino/memory-service/internal/registry/episodic"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
	registryvector "github.com/chirino/memory-service/internal/registry/vector"
	"github.com/google/uuid"
	"github.com/open-policy-agent/opa/v1/rego"
)

const migrationBatchSize = 50

// --- Typed migration error sentinels (Bug 6 fix) ---
// Each sentinel wraps the original error and carries a stable API-safe code.
// classifyMigrationError uses errors.As so no string matching against provider text occurs.

type migErrCASConflict struct{ cause error }
type migErrScanFailed struct{ cause error }
type migErrRescheduleFailed struct{ cause error }
type migErrLoadFailed struct{ cause error }
type migErrStateUpdateFailed struct{ cause error }

func (e *migErrCASConflict) Error() string       { return e.cause.Error() }
func (e *migErrCASConflict) Unwrap() error       { return e.cause }
func (e *migErrScanFailed) Error() string        { return e.cause.Error() }
func (e *migErrScanFailed) Unwrap() error        { return e.cause }
func (e *migErrRescheduleFailed) Error() string  { return e.cause.Error() }
func (e *migErrRescheduleFailed) Unwrap() error  { return e.cause }
func (e *migErrLoadFailed) Error() string        { return e.cause.Error() }
func (e *migErrLoadFailed) Unwrap() error        { return e.cause }
func (e *migErrStateUpdateFailed) Error() string { return e.cause.Error() }
func (e *migErrStateUpdateFailed) Unwrap() error { return e.cause }

// errMigrationTerminalFailed is a sentinel returned by executeKindMigrationBatch when the
// migration has been marked failed and the task should be deleted (not retried). The caller
// (processClaimedTask) detects this to emit ResultFailed rather than ResultSuccess.
var errMigrationTerminalFailed = errors.New("migration marked failed")

// Stable error codes for terminal migration failures.  These are sanitized, provider-text-free
// strings safe for API exposure.  Never derive a terminal code from error.Error() string content.
const (
	migErrDecryptFailed          = "decrypt_failed"
	migErrJSONDecodeFailed       = "json_decode_failed"
	migErrNamespaceDecodeFailed  = "namespace_decode_failed"
	migErrTargetSchemaMissing    = "target_schema_missing"
	migErrProjectionCompileFail  = "projection_compile_failed"
	migErrProjectionEvalFail     = "projection_eval_failed"
	migErrProjectionValidateFail = "projection_validate_failed"
)

// TaskProcessor polls for ready tasks and executes them. It processes
// vector_store_delete tasks by calling the vector store's delete method,
// and memory_kind_migration tasks by executing schema migration batches.
type TaskProcessor struct {
	store         registrystore.MemoryStore
	vector        registryvector.VectorStore
	episodicStore registryepisodic.EpisodicStore
	encSvc        *dataencryption.Service
	interval      time.Duration
	retryDelay    time.Duration
	batchSize     int
}

// NewTaskProcessor creates a new background task processor.
func NewTaskProcessor(store registrystore.MemoryStore, vector registryvector.VectorStore) *TaskProcessor {
	return &TaskProcessor{
		store:      store,
		vector:     vector,
		interval:   1 * time.Minute,
		retryDelay: 10 * time.Minute,
		batchSize:  100,
	}
}

// SetEpisodicStore wires an episodic store and encryption service so the task processor
// can execute memory_kind_migration tasks.
func (p *TaskProcessor) SetEpisodicStore(store registryepisodic.EpisodicStore, enc *dataencryption.Service) {
	p.episodicStore = store
	p.encSvc = enc
}

// Start begins the periodic task processing loop. Returns when ctx is cancelled.
func (p *TaskProcessor) Start(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.processBatch(ctx)
		}
	}
}

func (p *TaskProcessor) logErr(ctx context.Context, msg string, args ...any) {
	if ctx.Err() != nil {
		return // shutting down — suppress errors after context cancellation
	}
	log.Error(msg, args...)
}

func (p *TaskProcessor) processBatch(ctx context.Context) {
	if err := p.ProcessOnce(ctx); err != nil {
		p.logErr(ctx, "TaskProcessor: claim tasks failed", "err", err)
	}
}

// ProcessOnce claims and processes one batch of ready tasks. It is also used by
// deterministic integration tests that should not wait for the periodic ticker.
func (p *TaskProcessor) ProcessOnce(ctx context.Context) error {
	var tasks []model.Task
	err := p.store.InWriteTx(ctx, func(writeCtx context.Context) error {
		var err error
		tasks, err = p.store.ClaimReadyTasks(writeCtx, p.batchSize)
		return err
	})
	if err != nil {
		return err
	}
	for _, task := range tasks {
		p.processClaimedTask(ctx, task)
	}
	return nil
}

func (p *TaskProcessor) processClaimedTask(ctx context.Context, task model.Task) {
	event := operationevent.New(taskOperationName(task.TaskType))
	event.SetTaskID(task.ID.String())
	event.SetRetryAttempt(task.RetryCount + 1)
	defer recoverJobPanic(event, func() {
		event.SetFailureCount(1)
		event.EmitTerminal(operationevent.ResultFailed)
	})
	// Emit a start record for substantial (migration) tasks so operations are
	// observable in the audit log even when the batch is long-running.
	if task.TaskType == "memory_kind_migration" {
		event.EmitStart()
	}
	taskCtx := operationevent.WithContext(ctx, event)
	execErr := p.executeTask(taskCtx, task.TaskType, task.TaskBody)
	if execErr != nil {
		if result, interrupted := jobContextResult(ctx, execErr); interrupted {
			if result == operationevent.ResultTimedOut {
				event.SetReason("deadline")
			} else {
				event.SetReason("shutdown")
			}
			event.EmitTerminal(result)
			return
		}
		// Terminal failure sentinel: delete the task (no retry) but emit failed result.
		if errors.Is(execErr, errMigrationTerminalFailed) {
			event.SetFailureCount(1)
			if dErr := p.store.InWriteTx(ctx, func(writeCtx context.Context) error {
				return p.store.DeleteTask(writeCtx, task.ID)
			}); dErr != nil {
				p.logErr(ctx, "TaskProcessor: delete task failed after terminal migration failure", "taskId", task.ID, "err", dErr)
				event.SetReason("task_cleanup_failed")
				event.EnrichError(dErr)
			} else {
				event.SetReason("migration_failed")
			}
			event.EmitTerminal(operationevent.ResultFailed)
			return
		}
		p.logErr(ctx, "TaskProcessor: task failed", "taskId", task.ID, "type", task.TaskType, "err", execErr)
		// For migration tasks, persist the stable error code and retry counter on the
		// migration record so the API response reflects transient failure history.
		if task.TaskType == "memory_kind_migration" {
			migID := migrationIDFromBody(task.TaskBody)
			if migID != uuid.Nil && p.episodicStore != nil {
				// Sanitize: never persist raw errors or provider text; use a stable
				// classified code so the persisted field is safe to return via API.
				errCode := classifyMigrationError(execErr)
				if rErr := p.episodicStore.InWriteTx(ctx, func(txCtx context.Context) error {
					return p.episodicStore.UpdateMemoryKindMigrationRetry(txCtx, migID, errCode)
				}); rErr != nil {
					p.logErr(ctx, "TaskProcessor: update migration retry counters failed", "taskId", task.ID, "err", rErr)
				}
			}
		}
		if fErr := p.store.InWriteTx(ctx, func(writeCtx context.Context) error {
			return p.store.FailTask(writeCtx, task.ID, execErr.Error(), p.retryDelay)
		}); fErr != nil {
			p.logErr(ctx, "TaskProcessor: fail task record failed", "taskId", task.ID, "err", fErr)
			event.SetReason("retry_persistence_failed")
			event.EnrichError(fErr)
			event.EmitTerminal(operationevent.ResultFailed)
		} else {
			event.SetReason("retry_scheduled")
			event.EnrichError(execErr)
			event.EmitTerminal(operationevent.ResultRetrying)
		}
		return
	}
	if dErr := p.store.InWriteTx(ctx, func(writeCtx context.Context) error {
		return p.store.DeleteTask(writeCtx, task.ID)
	}); dErr != nil {
		p.logErr(ctx, "TaskProcessor: delete task failed", "taskId", task.ID, "err", dErr)
		event.SetReason("task_cleanup_failed")
		event.EnrichError(dErr)
		event.EmitTerminal(operationevent.ResultFailed)
	} else {
		event.EmitTerminal(operationevent.ResultSuccess)
	}
}

// migrationIDFromBody parses migration_id from a task body. Returns uuid.Nil on failure.
func migrationIDFromBody(body map[string]any) uuid.UUID {
	s, ok := body["migration_id"].(string)
	if !ok || s == "" {
		return uuid.Nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil
	}
	return id
}

// truncateErrorCode shortens an error string to at most maxLen runes for storage.
func truncateErrorCode(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) > maxLen {
		return string(runes[:maxLen])
	}
	return s
}

// classifyMigrationError maps a transient migration batch error to a stable,
// sanitized code safe for API exposure.  Raw error text (provider responses,
// stack frames, query details) is never persisted.
//
// Bug 6 fix: classification uses errors.As against typed sentinel types defined in
// this file.  No string matching against provider or error text.
func classifyMigrationError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	var casConflict *migErrCASConflict
	if errors.As(err, &casConflict) {
		return "cas_conflict"
	}
	var reschedule *migErrRescheduleFailed
	if errors.As(err, &reschedule) {
		return "reschedule_failed"
	}
	var scan *migErrScanFailed
	if errors.As(err, &scan) {
		return "scan_failed"
	}
	var load *migErrLoadFailed
	if errors.As(err, &load) {
		return "load_failed"
	}
	var stateUpdate *migErrStateUpdateFailed
	if errors.As(err, &stateUpdate) {
		return "state_update_failed"
	}
	return "transient_error"
}

func taskOperationName(taskType string) string {
	switch taskType {
	case "vector_store_delete":
		return "job.vector_store_delete"
	case "memory_kind_migration":
		return "job.memory_kind_migration"
	}
	return "job.task"
}

func (p *TaskProcessor) executeTask(ctx context.Context, taskType string, body map[string]any) error {
	switch taskType {
	case "vector_store_delete":
		return p.executeVectorStoreDelete(ctx, body)
	case "memory_kind_migration":
		return p.executeKindMigrationBatch(ctx, body)
	default:
		return fmt.Errorf("unknown task type: %s", taskType)
	}
}

func (p *TaskProcessor) executeVectorStoreDelete(ctx context.Context, body map[string]any) error {
	if p.vector == nil || !p.vector.IsEnabled() {
		return nil // skip silently — vector store not configured
	}
	groupIDStr, ok := body["conversationGroupId"].(string)
	if !ok {
		return fmt.Errorf("missing or invalid conversationGroupId in task body")
	}
	groupID, err := uuid.Parse(groupIDStr)
	if err != nil {
		return fmt.Errorf("invalid conversationGroupId %q: %w", groupIDStr, err)
	}
	return p.vector.DeleteByConversationGroupID(ctx, groupID)
}

// executeKindMigrationBatch processes one bounded batch of a schema migration job.
// On success (rows found and processed), the task is rescheduled as a continuation
// so the next batch runs without incrementing the retry count.
// On an empty scan, the migration is marked succeeded and the task is deleted.
//
// All reads and writes to the episodic store are wrapped in InReadTx / InWriteTx so
// the SQLite store cannot panic from unscoped access.
func (p *TaskProcessor) executeKindMigrationBatch(ctx context.Context, body map[string]any) error {
	if p.episodicStore == nil {
		return nil // episodic store not configured; skip
	}

	migrationIDStr, ok := body["migration_id"].(string)
	if !ok {
		return fmt.Errorf("missing migration_id in task body")
	}
	migrationID, err := uuid.Parse(migrationIDStr)
	if err != nil {
		return fmt.Errorf("invalid migration_id %q: %w", migrationIDStr, err)
	}

	// Decode the afterID cursor from the task body (empty UUID = start from beginning).
	afterIDStr, _ := body["after_id"].(string)
	var afterID uuid.UUID
	if afterIDStr != "" {
		afterID, err = uuid.Parse(afterIDStr)
		if err != nil {
			afterID = uuid.Nil
		}
	}
	var afterCreatedAt time.Time
	if raw, ok := body["after_created_at"].(string); ok && raw != "" {
		afterCreatedAt, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return fmt.Errorf("invalid after_created_at %q: %w", raw, err)
		}
	}

	// Load the migration record inside a read tx scope (required by SQLite).
	var m *model.MemoryKindMigration
	if rErr := p.episodicStore.InReadTx(ctx, func(txCtx context.Context) error {
		var innerErr error
		m, innerErr = p.episodicStore.GetMemoryKindMigration(txCtx, migrationID)
		return innerErr
	}); rErr != nil {
		return &migErrLoadFailed{cause: fmt.Errorf("load migration: %w", rErr)}
	}
	if m == nil {
		return nil // migration was deleted; nothing to do
	}

	// Terminal states: migration already finished (failed/succeeded/canceled).
	// Return nil so the orphaned task is deleted without re-running.
	if m.State == model.MigrationStateFailed || m.State == model.MigrationStateSucceeded || m.State == model.MigrationStateCanceled {
		return nil
	}

	// Cancellation check — write tx required to persist state change.
	if m.CancelRequested || m.State == model.MigrationStateCanceling {
		now := time.Now().UTC()
		if stErr := p.episodicStore.InWriteTx(ctx, func(txCtx context.Context) error {
			return p.episodicStore.UpdateMemoryKindMigrationState(txCtx, migrationID, model.MigrationStateCanceled, nil, &now)
		}); stErr != nil {
			return &migErrStateUpdateFailed{cause: fmt.Errorf("kind migration: persist canceled state: %w", stErr)}
		}
		return nil // tell the processor the task succeeded (delete it)
	}

	// Mark running on first batch (state may be queued) — write tx required.
	if m.State == model.MigrationStateQueued {
		now := time.Now().UTC()
		if stErr := p.episodicStore.InWriteTx(ctx, func(txCtx context.Context) error {
			return p.episodicStore.UpdateMemoryKindMigrationState(txCtx, migrationID, model.MigrationStateRunning, &now, nil)
		}); stErr != nil {
			if errors.Is(stErr, registryepisodic.ErrMemoryKindMigrationStateConflict) {
				stopped, checkErr := p.cancelMigrationIfRequested(ctx, migrationID)
				if checkErr != nil {
					return checkErr
				}
				if stopped {
					return nil
				}
			}
			return &migErrStateUpdateFailed{cause: fmt.Errorf("kind migration: persist running state: %w", stErr)}
		}
	}

	// Load target schema version for projection — read tx required.
	var sv *model.MemoryKindVersion
	if rErr := p.episodicStore.InReadTx(ctx, func(txCtx context.Context) error {
		var innerErr error
		sv, innerErr = p.episodicStore.GetMemoryKindVersion(txCtx, m.Target)
		return innerErr
	}); rErr != nil {
		return &migErrLoadFailed{cause: fmt.Errorf("load target schema version: %w", rErr)}
	}
	if sv == nil {
		log.Error("kind migration: target schema version not found", "migration", migrationID, "target", m.Target)
		return p.markMigrationFailed(ctx, migrationID, migErrTargetSchemaMissing)
	}

	// Compile target projection program (may be nil if no Rego).
	var pq *rego.PreparedEvalQuery
	if sv.AttributesRego != nil && *sv.AttributesRego != "" {
		pq, err = episodic.CompileKindProjection(ctx, *sv.AttributesRego)
		if err != nil {
			log.Error("kind migration: projection compile failed", "migration", migrationID, "target", m.Target, "err", err)
			return p.markMigrationFailed(ctx, migrationID, migErrProjectionCompileFail)
		}
	}

	// Scan up to migrationBatchSize rows — read tx required by SQLite.
	var candidates []registryepisodic.MigrationCandidate
	if rErr := p.episodicStore.InReadTx(ctx, func(txCtx context.Context) error {
		var innerErr error
		candidates, innerErr = p.episodicStore.FindMemoriesToMigrateByKind(txCtx, m.Source, m.NamespacePrefix, afterCreatedAt, afterID, migrationBatchSize)
		return innerErr
	}); rErr != nil {
		return &migErrScanFailed{cause: fmt.Errorf("find memories to migrate: %w", rErr)}
	}

	if len(candidates) == 0 {
		// Cursor is exhausted.  Run the full paginated verification sweep AND the final
		// state mutation (either restart-task or succeed) inside ONE episodicStore.InWriteTx
		// so that for SQL stores the reads and the terminal write are in the same transaction.
		//
		// Design:
		//   - Tombstones are never delta-counted during ordinary batches (replay-unsafe).
		//   - The absolute count is computed once here from the full sweep.
		//   - For PostgreSQL: postgresEpisodicStore.InWriteTx opens a GORM transaction and
		//     stores the scoped *gorm.DB in txCtx via the package-level scopeKey.
		//     p.store.CreateTask(txCtx, ...) calls PostgresStore.InWriteTx, which finds the
		//     same scope in txCtx and joins the open transaction rather than starting a new
		//     one.  Both the episodic sweep writes and the restart enqueue are therefore
		//     fully atomic in a single Postgres transaction.
		//   - For SQLite: sqliteEpisodicStore.InWriteTx delegates to sharedHandle.InWriteTx,
		//     which stores the write scope in txCtx.  p.store.CreateTask(txCtx, ...) resolves
		//     the same sharedHandle scope and joins the same SQLite write transaction.
		//   - Mongo InWriteTx opens a session transaction; reads and writes using txCtx
		//     join that transaction across the memories, migration, and task collections.
		canceledDuringSweep := false
		if sweepErr := p.episodicStore.InWriteTx(ctx, func(txCtx context.Context) error {
			var absoluteTombstoneCount int64
			hasReplayable := false
			verAfterID := uuid.Nil
			var verAfterCreatedAt time.Time
			for {
				if txCtx.Err() != nil {
					return txCtx.Err()
				}
				current, innerErr := p.episodicStore.GetMemoryKindMigration(txCtx, migrationID)
				if innerErr != nil {
					return &migErrLoadFailed{cause: innerErr}
				}
				if current == nil || current.State == model.MigrationStateCanceled || current.State == model.MigrationStateSucceeded || current.State == model.MigrationStateFailed {
					canceledDuringSweep = true
					return nil
				}
				if current.CancelRequested || current.State == model.MigrationStateCanceling {
					now := time.Now().UTC()
					if innerErr := p.episodicStore.UpdateMemoryKindMigrationState(txCtx, migrationID, model.MigrationStateCanceled, nil, &now); innerErr != nil && !errors.Is(innerErr, registryepisodic.ErrMemoryKindMigrationStateConflict) {
						return innerErr
					}
					canceledDuringSweep = true
					return nil
				}
				verBatch, innerErr := p.episodicStore.FindMemoriesToMigrateByKind(txCtx, m.Source, m.NamespacePrefix, verAfterCreatedAt, verAfterID, migrationBatchSize)
				if innerErr != nil {
					return &migErrScanFailed{cause: fmt.Errorf("kind migration: verification sweep failed: %w", innerErr)}
				}
				if len(verBatch) == 0 {
					break // sweep complete
				}
				for _, row := range verBatch {
					if len(row.ValueEncrypted) > 0 {
						hasReplayable = true
						// Stop counting — we will restart; tombstones will be re-counted.
						break
					}
					absoluteTombstoneCount++
				}
				if hasReplayable {
					break
				}
				verAfterID = verBatch[len(verBatch)-1].ID
				verAfterCreatedAt = verBatch[len(verBatch)-1].CreatedAt
			}

			if hasReplayable {
				// Replayable rows still exist; schedule a full restart from nil cursor.
				// Do NOT update counters — tombstones will be re-counted on the next sweep.
				restartBody := make(map[string]any, len(body))
				for k, v := range body {
					if k != "taskName" && k != "after_id" && k != "after_created_at" {
						restartBody[k] = v
					}
				}
				restartBody["taskName"] = migrationContinuationTaskName(migrationID, "restart", afterCreatedAt, afterID)
				// CreateTask(txCtx, ...) joins the open transaction for PostgreSQL, SQLite,
				// and MongoDB through their datastore-specific scoped transaction contexts.
				if cErr := p.episodicStore.CreateMemoryKindMigrationTask(txCtx, restartBody); cErr != nil {
					return &migErrRescheduleFailed{cause: fmt.Errorf("kind migration: reschedule restart: %w", cErr)}
				}
				return nil
			}

			// No replayable rows remain.  Persist state=succeeded with the ABSOLUTE
			// skipped_tombstone_count from this sweep.  The operation is idempotent:
			// retrying the task re-sweeps and overwrites with the same count.
			now := time.Now().UTC()
			if stErr := p.episodicStore.UpdateMemoryKindMigrationSucceeded(txCtx, migrationID, now, absoluteTombstoneCount); stErr != nil {
				return &migErrStateUpdateFailed{cause: fmt.Errorf("kind migration: persist succeeded state: %w", stErr)}
			}
			return nil
		}); sweepErr != nil {
			if errors.Is(sweepErr, registryepisodic.ErrMemoryKindMigrationStateConflict) {
				stopped, checkErr := p.cancelMigrationIfRequested(ctx, migrationID)
				if checkErr != nil {
					return checkErr
				}
				if stopped {
					return nil
				}
			}
			return sweepErr
		}
		if canceledDuringSweep {
			return nil
		}
		return nil // task will be deleted by the processor
	}

	// Process each candidate in the batch.
	//
	// Design:
	// - Tombstones advance the cursor but are NOT delta-counted; the absolute count
	//   is computed once during the final verification sweep (replay-safe).
	// - For each live row, MigrateOneMemoryKindCAS and migrated_count += 1 execute
	//   in the SAME episodicStore.InWriteTx so both are atomic for SQL stores.
	// - On any CAS revision conflict, stop immediately and return a typed error so
	//   the task retries.  Do not process later rows and do not advance the cursor
	//   past the conflict.  Already-migrated earlier rows no longer appear in the
	//   source scan on retry.
	// - Continuation creation contains only cursor scheduling; no counter updates.

	var lastProcessedID uuid.UUID

	for _, c := range candidates {
		stopped, checkErr := p.cancelMigrationIfRequested(ctx, migrationID)
		if checkErr != nil {
			return checkErr
		}
		if stopped {
			return nil
		}
		// Tombstones cannot be replayed; advance cursor, do NOT count tombstone delta.
		if len(c.ValueEncrypted) == 0 {
			lastProcessedID = c.ID
			continue
		}

		// Decrypt the memory value.
		decrypted, decErr := p.decryptValue(c.ID, c.ValueEncrypted)
		if decErr != nil {
			log.Error("kind migration: decrypt failed for memory", "memory", c.ID, "migration", migrationID, "err", decErr)
			return p.markMigrationFailed(ctx, migrationID, migErrDecryptFailed)
		}

		// Decode the JSON value.
		var value map[string]interface{}
		if err := json.Unmarshal(decrypted, &value); err != nil {
			log.Error("kind migration: value unmarshal failed for memory", "memory", c.ID, "migration", migrationID, "err", err)
			return p.markMigrationFailed(ctx, migrationID, migErrJSONDecodeFailed)
		}

		// Decode namespace.
		ns, nsErr := episodic.DecodeNamespace(c.Namespace)
		if nsErr != nil {
			log.Error("kind migration: namespace decode failed for memory", "memory", c.ID, "migration", migrationID, "err", nsErr)
			return p.markMigrationFailed(ctx, migrationID, migErrNamespaceDecodeFailed)
		}

		// Evaluate the target projection.
		var newAttrs map[string]interface{}
		if pq != nil {
			raw, evalErr := episodic.EvaluateKindProjection(ctx, pq, ns, c.Key, value, c.IndexedContent)
			if evalErr != nil {
				log.Error("kind migration: projection eval failed for memory", "memory", c.ID, "migration", migrationID, "err", evalErr)
				return p.markMigrationFailed(ctx, migrationID, migErrProjectionEvalFail)
			}
			newAttrs, err = episodic.ValidateAndNormalizeKindProjection(raw, sv.AttributeTypes)
			if err != nil {
				log.Error("kind migration: projection validation failed for memory", "memory", c.ID, "migration", migrationID, "err", err)
				return p.markMigrationFailed(ctx, migrationID, migErrProjectionValidateFail)
			}
		} else {
			newAttrs = map[string]interface{}{}
		}

		// Atomic CAS cutover: MigrateOneMemoryKindCAS + migrated_count += 1 in one
		// datastore transaction on PostgreSQL, SQLite, and MongoDB.
		casErr := p.episodicStore.InWriteTx(ctx, func(txCtx context.Context) error {
			if cErr := p.episodicStore.MigrateOneMemoryKindCAS(txCtx, c.ID, c.MemoryKind, c.Revision, newAttrs, m.Target); cErr != nil {
				return cErr
			}
			return p.episodicStore.UpdateMemoryKindMigrationIncrementMigrated(txCtx, migrationID)
		})
		if casErr != nil {
			if errors.Is(casErr, registryepisodic.ErrMemoryRevisionConflict) {
				// Row was updated concurrently.  Stop the batch immediately and return a
				// typed CAS error so the task retries.  Do NOT advance lastProcessedID
				// past this row.  Already-migrated rows no longer match source on retry.
				return &migErrCASConflict{cause: fmt.Errorf("CAS conflict for memory %v; retrying", c.ID)}
			}
			return &migErrCASConflict{cause: fmt.Errorf("CAS cutover for memory %v: %w", c.ID, casErr)}
		}
		lastProcessedID = c.ID
	}

	// If every row in the batch was a tombstone, lastProcessedID is the last tombstone.
	// If lastProcessedID is still zero-value, all rows had some other issue — return error.
	if lastProcessedID == (uuid.UUID{}) {
		return &migErrCASConflict{cause: fmt.Errorf("kind migration: no progress in batch of %d rows; retrying", len(candidates))}
	}

	// Schedule the next-batch continuation containing only the new cursor.
	// No counter updates here — migrated_count is incremented atomically with each CAS
	// and tombstones are counted only during finalization.
	contBody := make(map[string]any, len(body))
	for k, v := range body {
		if k != "taskName" {
			contBody[k] = v
		}
	}
	contBody["after_id"] = lastProcessedID.String()
	for i := len(candidates) - 1; i >= 0; i-- {
		if candidates[i].ID == lastProcessedID {
			contBody["after_created_at"] = candidates[i].CreatedAt.UTC().Format(time.RFC3339Nano)
			contBody["taskName"] = migrationContinuationTaskName(migrationID, "after", candidates[i].CreatedAt, lastProcessedID)
			break
		}
	}
	if err := p.episodicStore.InWriteTx(ctx, func(txCtx context.Context) error {
		return p.episodicStore.CreateMemoryKindMigrationTask(txCtx, contBody)
	}); err != nil {
		// Return an error so the current task retries.  Safe: migrated rows have already
		// incremented their counter; the continuation task will simply restart from
		// the same cursor and those rows will no longer appear in the source scan.
		return &migErrRescheduleFailed{cause: fmt.Errorf("kind migration: reschedule failed: %w", err)}
	}
	return nil
}

func migrationContinuationTaskName(migrationID uuid.UUID, phase string, createdAt time.Time, id uuid.UUID) string {
	return fmt.Sprintf("migration:%s:%s:%d:%s", migrationID, phase, createdAt.UTC().UnixNano(), id)
}

func (p *TaskProcessor) cancelMigrationIfRequested(ctx context.Context, migrationID uuid.UUID) (bool, error) {
	var current *model.MemoryKindMigration
	if err := p.episodicStore.InReadTx(ctx, func(txCtx context.Context) error {
		var loadErr error
		current, loadErr = p.episodicStore.GetMemoryKindMigration(txCtx, migrationID)
		return loadErr
	}); err != nil {
		return false, &migErrLoadFailed{cause: fmt.Errorf("reload migration cancellation: %w", err)}
	}
	if current == nil || current.State == model.MigrationStateCanceled || current.State == model.MigrationStateSucceeded || current.State == model.MigrationStateFailed {
		return true, nil
	}
	if !current.CancelRequested && current.State != model.MigrationStateCanceling {
		return false, nil
	}
	now := time.Now().UTC()
	err := p.episodicStore.InWriteTx(ctx, func(txCtx context.Context) error {
		return p.episodicStore.UpdateMemoryKindMigrationState(txCtx, migrationID, model.MigrationStateCanceled, nil, &now)
	})
	if err != nil && !errors.Is(err, registryepisodic.ErrMemoryKindMigrationStateConflict) {
		return false, &migErrStateUpdateFailed{cause: fmt.Errorf("persist canceled state: %w", err)}
	}
	return true, nil
}

const memoryValueFieldDomain = "memory.value"

func (p *TaskProcessor) decryptValue(id uuid.UUID, ciphertext []byte) ([]byte, error) {
	if p.encSvc == nil || len(ciphertext) == 0 {
		return ciphertext, nil
	}
	return p.encSvc.DecryptField(ciphertext, memoryValueFieldDomain, strings.ToLower(id.String()))
}

// markMigrationFailed atomically sets state=failed + last_error_code + completed_at on the
// migration record and returns errMigrationTerminalFailed so the task is deleted without retry.
// The stable errorCode constant (never raw provider text) is persisted.
func (p *TaskProcessor) markMigrationFailed(ctx context.Context, migrationID uuid.UUID, errorCode string) error {
	now := time.Now().UTC()
	if stErr := p.episodicStore.InWriteTx(ctx, func(txCtx context.Context) error {
		return p.episodicStore.UpdateMemoryKindMigrationStateFailed(txCtx, migrationID, errorCode, now)
	}); stErr != nil {
		return fmt.Errorf("kind migration: persist failed state (%s): %w", errorCode, stErr)
	}
	return errMigrationTerminalFailed
}
