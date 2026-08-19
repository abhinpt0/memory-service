//go:build !nopostgresql

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/chirino/memory-service/internal/episodic"
	"github.com/chirino/memory-service/internal/model"
	registryepisodic "github.com/chirino/memory-service/internal/registry/episodic"
	"github.com/google/uuid"
)

// CreateMemoryKindVersion persists an immutable schema version.
// Returns the existing resource if identical, ErrMemoryKindVersionConflict if different.
//
// Defect 11 fix: uses INSERT ... ON CONFLICT DO NOTHING so concurrent races do not
// abort the surrounding Postgres transaction (unlike reading then inserting which
// hits 23505 UniqueViolation after the tx is already poisoned).
func (e *postgresEpisodicStore) CreateMemoryKindVersion(ctx context.Context, version model.MemoryKindVersion) (*model.MemoryKindVersion, error) {
	db, err := e.s.writeDBFor(ctx, "CreateMemoryKindVersion")
	if err != nil {
		return nil, err
	}
	// Attempt an insert; on conflict (duplicate name) do nothing.
	attrTypesJSON, err := json.Marshal(version.AttributeTypes)
	if err != nil {
		return nil, err
	}
	regoVal := interface{}(nil)
	if version.AttributesRego != nil {
		regoVal = *version.AttributesRego
	}
	res := db.Exec(
		`INSERT INTO memory_kind_versions (name, attribute_types, attributes_rego, writable, created_at)
		 VALUES (?, ?::jsonb, ?, ?, ?)
		 ON CONFLICT (name) DO NOTHING`,
		version.Name, string(attrTypesJSON), regoVal, version.Writable, version.CreatedAt,
	)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected > 0 {
		// New row inserted — return as-is.
		return &version, nil
	}
	// Row already existed; reload to check idempotency without risking an aborted tx.
	var existing model.MemoryKindVersion
	reload := db.Table("memory_kind_versions").Where("name = ?", version.Name).Limit(1).Find(&existing)
	if reload.Error != nil {
		return nil, reload.Error
	}
	if reload.RowsAffected == 0 {
		// Extremely unlikely — another transaction deleted the row between our insert
		// and this read.  Retry by returning a conflict error so the caller can retry.
		return nil, registryepisodic.ErrMemoryKindVersionConflict
	}
	if !schemaVersionsEqual(existing, version) {
		return nil, registryepisodic.ErrMemoryKindVersionConflict
	}
	return &existing, nil
}

// GetMemoryKindVersion retrieves a schema version by canonical name.
func (e *postgresEpisodicStore) GetMemoryKindVersion(ctx context.Context, name string) (*model.MemoryKindVersion, error) {
	db := e.s.dbFor(ctx)
	var v model.MemoryKindVersion
	res := db.Table("memory_kind_versions").Where("name = ?", name).Limit(1).Find(&v)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	return &v, nil
}

// ListMemoryKindVersions lists schema versions, optionally filtered by family.
func (e *postgresEpisodicStore) ListMemoryKindVersions(ctx context.Context, family string) ([]model.MemoryKindVersion, error) {
	db := e.s.dbFor(ctx)
	q := db.Table("memory_kind_versions").Order("name ASC")
	if family != "" {
		q = q.Where("name LIKE ?", family+"/%")
	}
	var versions []model.MemoryKindVersion
	if err := q.Find(&versions).Error; err != nil {
		return nil, err
	}
	return versions, nil
}

// CreateMemoryKindMigration persists a new migration.
//
// Defect 11 fix: the unique partial index on (source) WHERE state IN ('queued',
// 'running', 'canceling') enforces exactly-one-active semantics.  A concurrent
// race on the unique-index violation would poison the surrounding Postgres
// transaction, so we check for the active migration and translate the 23505
// unique-violation from the INSERT into ErrMemoryKindMigrationActiveForSource
// rather than doing a read-before-write.
func (e *postgresEpisodicStore) CreateMemoryKindMigration(ctx context.Context, m model.MemoryKindMigration) (*model.MemoryKindMigration, error) {
	db, err := e.s.writeDBFor(ctx, "CreateMemoryKindMigration")
	if err != nil {
		return nil, err
	}
	// First check: avoid inserting when an active migration already exists so we
	// can give a friendlier error without relying on the unique index violation.
	var existing model.MemoryKindMigration
	res := db.Table("memory_kind_migrations").
		Where("source = ? AND state IN ('queued','running','canceling')", m.Source).
		Limit(1).Find(&existing)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected > 0 {
		return nil, registryepisodic.ErrMemoryKindMigrationActiveForSource
	}
	// Proceed with insert; rely on the unique partial index to catch concurrent races.
	if err := db.Table("memory_kind_migrations").Create(&m).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, registryepisodic.ErrMemoryKindMigrationActiveForSource
		}
		return nil, err
	}
	return &m, nil
}

func (e *postgresEpisodicStore) CreateMemoryKindMigrationAndTask(ctx context.Context, m model.MemoryKindMigration) (*model.MemoryKindMigration, error) {
	created, err := e.CreateMemoryKindMigration(ctx, m)
	if err != nil {
		return nil, err
	}
	taskName := "migration:" + created.ID.String()
	task := model.Task{
		ID: uuid.New(), TaskName: &taskName, TaskType: "memory_kind_migration",
		TaskBody:  map[string]interface{}{"migration_id": created.ID.String(), "taskName": taskName},
		CreatedAt: time.Now().UTC(), RetryAt: time.Now().UTC(),
	}
	if err := e.s.dbFor(ctx).Create(&task).Error; err != nil {
		return nil, fmt.Errorf("create initial migration task: %w", err)
	}
	return created, nil
}

func (e *postgresEpisodicStore) CreateMemoryKindMigrationTask(ctx context.Context, body map[string]interface{}) error {
	var taskName *string
	if raw, ok := body["taskName"].(string); ok && strings.TrimSpace(raw) != "" {
		name := strings.TrimSpace(raw)
		taskName = &name
	}
	task := model.Task{ID: uuid.New(), TaskName: taskName, TaskType: "memory_kind_migration", TaskBody: body, CreatedAt: time.Now().UTC(), RetryAt: time.Now().UTC()}
	err := e.s.dbFor(ctx).Create(&task).Error
	if taskName != nil && isUniqueViolation(err) {
		return nil
	}
	return err
}

// DeleteMemoryKindMigration permanently removes a migration record by UUID.
func (e *postgresEpisodicStore) DeleteMemoryKindMigration(ctx context.Context, id uuid.UUID) error {
	db, err := e.s.writeDBFor(ctx, "DeleteMemoryKindMigration")
	if err != nil {
		return err
	}
	return db.Table("memory_kind_migrations").Where("id = ?", id).Delete(nil).Error
}

// GetMemoryKindMigration retrieves a migration by UUID.
func (e *postgresEpisodicStore) GetMemoryKindMigration(ctx context.Context, id uuid.UUID) (*model.MemoryKindMigration, error) {
	db := e.s.dbFor(ctx)
	var m model.MemoryKindMigration
	res := db.Table("memory_kind_migrations").Where("id = ?", id).Limit(1).Find(&m)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	return &m, nil
}

// ListMemoryKindMigrations lists migrations, optionally filtered by state.
func (e *postgresEpisodicStore) ListMemoryKindMigrations(ctx context.Context, state string) ([]model.MemoryKindMigration, error) {
	db := e.s.dbFor(ctx)
	q := db.Table("memory_kind_migrations").Order("created_at DESC")
	if state != "" {
		q = q.Where("state = ?", state)
	}
	var migrations []model.MemoryKindMigration
	if err := q.Find(&migrations).Error; err != nil {
		return nil, err
	}
	return migrations, nil
}

// UpdateMemoryKindMigrationCancelRequested marks a queued or running migration as canceling.
// Returns ErrMemoryKindMigrationNotFound if no migration with the given ID exists.
func (e *postgresEpisodicStore) UpdateMemoryKindMigrationCancelRequested(ctx context.Context, id uuid.UUID) error {
	db, err := e.s.writeDBFor(ctx, "UpdateMemoryKindMigrationCancelRequested")
	if err != nil {
		return err
	}
	res := db.Table("memory_kind_migrations").
		Where("id = ? AND state IN ('queued','running')", id).
		Updates(map[string]interface{}{"cancel_requested": true, "state": "canceling"})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		return nil
	}
	// No rows updated: either ID doesn't exist or migration is already in a terminal/canceling state.
	// Distinguish the two by checking existence.
	m, err := e.GetMemoryKindMigration(ctx, id)
	if err != nil {
		return err
	}
	if m == nil {
		return registryepisodic.ErrMemoryKindMigrationNotFound
	}
	// Already canceling/canceled/succeeded/failed — idempotent no-op.
	return nil
}

// UpdateMemoryKindMigrationState updates state and timestamps.
// Uses writeDBFor so it participates in the enclosing InWriteTx transaction.
func (e *postgresEpisodicStore) UpdateMemoryKindMigrationState(ctx context.Context, id uuid.UUID, state string, startedAt, completedAt *time.Time) error {
	db, err := e.s.writeDBFor(ctx, "UpdateMemoryKindMigrationState")
	if err != nil {
		return err
	}
	updates := map[string]interface{}{"state": state}
	if startedAt != nil {
		updates["started_at"] = *startedAt
	}
	if completedAt != nil {
		updates["completed_at"] = *completedAt
	}
	q := db.Table("memory_kind_migrations").Where("id = ?", id)
	switch state {
	case model.MigrationStateRunning:
		q = q.Where("state = ? AND cancel_requested = FALSE", model.MigrationStateQueued)
	case model.MigrationStateCanceled:
		q = q.Where("state IN ? AND cancel_requested = TRUE", []string{model.MigrationStateQueued, model.MigrationStateRunning, model.MigrationStateCanceling})
	}
	res := q.Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return registryepisodic.ErrMemoryKindMigrationStateConflict
	}
	return nil
}

// UpdateMemoryKindMigrationStateFailed atomically sets state=failed, last_error_code, and completed_at.
// Uses writeDBFor so it participates in the enclosing InWriteTx transaction.
func (e *postgresEpisodicStore) UpdateMemoryKindMigrationStateFailed(ctx context.Context, id uuid.UUID, errorCode string, completedAt time.Time) error {
	db, err := e.s.writeDBFor(ctx, "UpdateMemoryKindMigrationStateFailed")
	if err != nil {
		return err
	}
	return db.Exec(
		`UPDATE memory_kind_migrations SET state = 'failed', last_error_code = ?, completed_at = ? WHERE id = ?`,
		errorCode, completedAt, id,
	).Error
}

// UpdateMemoryKindMigrationIncrementMigrated increments migrated_count by 1.
// Must be called inside the same InWriteTx as MigrateOneMemoryKindCAS so both roll back
// together if the transaction fails (e.g. due to a subsequent error in the same callback).
func (e *postgresEpisodicStore) UpdateMemoryKindMigrationIncrementMigrated(ctx context.Context, id uuid.UUID) error {
	db, err := e.s.writeDBFor(ctx, "UpdateMemoryKindMigrationIncrementMigrated")
	if err != nil {
		return err
	}
	return db.Exec(
		`UPDATE memory_kind_migrations SET migrated_count = migrated_count + 1 WHERE id = ?`,
		id,
	).Error
}

// UpdateMemoryKindMigrationSucceeded atomically sets state=succeeded, completed_at, and
// the ABSOLUTE skipped_tombstone_count observed during the final verification sweep.
// Idempotent: re-running overwrites with the same absolute count rather than accumulating.
// Uses writeDBFor so it participates in the enclosing InWriteTx transaction.
func (e *postgresEpisodicStore) UpdateMemoryKindMigrationSucceeded(ctx context.Context, id uuid.UUID, completedAt time.Time, absoluteSkippedTombstoneCount int64) error {
	db, err := e.s.writeDBFor(ctx, "UpdateMemoryKindMigrationSucceeded")
	if err != nil {
		return err
	}
	res := db.Exec(
		`UPDATE memory_kind_migrations SET
		   state = 'succeeded',
		   completed_at = ?,
		   skipped_tombstone_count = ?
		 WHERE id = ? AND state = 'running' AND cancel_requested = FALSE`,
		completedAt, absoluteSkippedTombstoneCount, id,
	)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return registryepisodic.ErrMemoryKindMigrationStateConflict
	}
	return nil
}

// UpdateMemoryKindMigrationCounters increments progress counters (used by indexer path).
// Uses writeDBFor so it participates in the enclosing InWriteTx transaction.
func (e *postgresEpisodicStore) UpdateMemoryKindMigrationCounters(ctx context.Context, id uuid.UUID, migratedDelta, tombstoneDelta, vectorPendingDelta int64) error {
	db, err := e.s.writeDBFor(ctx, "UpdateMemoryKindMigrationCounters")
	if err != nil {
		return err
	}
	return db.Exec(
		`UPDATE memory_kind_migrations SET
		   migrated_count = migrated_count + ?,
		   skipped_tombstone_count = skipped_tombstone_count + ?,
		   vector_pending_count = vector_pending_count + ?
		 WHERE id = ?`,
		migratedDelta, tombstoneDelta, vectorPendingDelta, id,
	).Error
}

// UpdateMemoryKindMigrationRetry increments retry_count and sets last_error_code.
// Uses writeDBFor so it participates in the enclosing InWriteTx transaction.
func (e *postgresEpisodicStore) UpdateMemoryKindMigrationRetry(ctx context.Context, id uuid.UUID, errorCode string) error {
	db, err := e.s.writeDBFor(ctx, "UpdateMemoryKindMigrationRetry")
	if err != nil {
		return err
	}
	return db.Exec(
		`UPDATE memory_kind_migrations SET retry_count = retry_count + 1, last_error_code = ? WHERE id = ?`,
		errorCode, id,
	).Error
}

// ResolveKindForWrite resolves the canonical schema name for a write.
func (e *postgresEpisodicStore) ResolveKindForWrite(ctx context.Context, sel string) (string, error) {
	canonicalName := strings.TrimSpace(sel)
	if canonicalName == "" {
		canonicalName = episodic.DefaultKindName
	}
	if _, _, err := episodic.ParseCanonicalKindName(canonicalName); err != nil {
		return "", fmt.Errorf("%w: %v", registryepisodic.ErrMemoryKindInvalid, err)
	}
	v, err := e.GetMemoryKindVersion(ctx, canonicalName)
	if err != nil {
		return "", err
	}
	if v == nil {
		return "", fmt.Errorf("%w: %s", registryepisodic.ErrMemoryKindNotFound, canonicalName)
	}
	if !v.Writable {
		return "", fmt.Errorf("%w: %s", registryepisodic.ErrMemoryKindNotWritable, canonicalName)
	}
	return canonicalName, nil
}

// SetMemoryKindIndexedAtCAS sets indexed_at on a memory row only if schema and revision still match.
// The archived_at lifecycle column is intentionally not guarded here because the indexer calls this
// for both active rows (embed/upsert) and archived/tombstone rows (vector cleanup). Filtering by
// archived_at would cause every post-archive vector-cleanup CAS to return ErrMemoryRevisionConflict,
// leaving indexed_at null forever and preventing subsequent eviction.
func (e *postgresEpisodicStore) SetMemoryKindIndexedAtCAS(ctx context.Context, memoryID uuid.UUID, expectedKind string, expectedRevision int64, indexedAt time.Time) error {
	db, err := e.s.writeDBFor(ctx, "SetMemoryKindIndexedAtCAS")
	if err != nil {
		return err
	}
	schemaCondition := "memory_kind = ?"
	res := db.Exec(
		`UPDATE memories SET indexed_at = ? WHERE id = ? AND revision = ? AND `+schemaCondition,
		indexedAt, memoryID, expectedRevision, expectedKind,
	)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return registryepisodic.ErrMemoryRevisionConflict
	}
	return nil
}

// schemaVersionsEqual compares two MemoryKindVersion records for idempotency.
func schemaVersionsEqual(a, b model.MemoryKindVersion) bool {
	if a.Writable != b.Writable {
		return false
	}
	if len(a.AttributeTypes) != len(b.AttributeTypes) {
		return false
	}
	for k, v := range a.AttributeTypes {
		if b.AttributeTypes[k] != v {
			return false
		}
	}
	aRego := ""
	bRego := ""
	if a.AttributesRego != nil {
		aRego = *a.AttributesRego
	}
	if b.AttributesRego != nil {
		bRego = *b.AttributesRego
	}
	return aRego == bRego
}

// FindMemoriesToMigrateByKind returns up to limit memory rows whose effective schema is sourceKind.
// Rows with an id > afterID are returned (empty UUID = start from beginning).
// Tombstones (nil ValueEncrypted) are included; the absolute tombstone count is computed during
// the final verification sweep in executeKindMigrationBatch, not per page.
// Uses dbFor so it can participate in a read or write transaction scope.
func (e *postgresEpisodicStore) FindMemoriesToMigrateByKind(ctx context.Context, sourceKind string, namespacePrefix []string, afterCreatedAt time.Time, afterID uuid.UUID, limit int) ([]registryepisodic.MigrationCandidate, error) {
	db := e.s.dbFor(ctx)
	schemaWhere := "memory_kind = ?"
	args := []interface{}{sourceKind}
	// Only the current (non-superseded) row for each key is eligible: archived_at IS NULL OR deleted_reason = 1 (archived by user).
	// Tombstones have archived_at IS NOT NULL AND deleted_reason != 1; we include them separately to count them.
	whereClause := schemaWhere + " AND (created_at > ? OR (created_at = ? AND id > ?))"
	args = append(args, afterCreatedAt, afterCreatedAt, afterID)
	if len(namespacePrefix) > 0 {
		encoded, err := episodic.EncodeNamespace(namespacePrefix, 0)
		if err != nil {
			return nil, err
		}
		// Match the exact encoded namespace OR any descendant separated by the RS byte.
		// Using NamespacePrefixPattern avoids matching a sibling like ["user","alice"] when
		// the prefix is ["user","a"].
		whereClause += " AND (namespace = ? OR namespace LIKE ?)"
		args = append(args, encoded, episodic.NamespacePrefixPattern(encoded))
	}
	type scanRow struct {
		ID               uuid.UUID              `gorm:"column:id"`
		Namespace        string                 `gorm:"column:namespace"`
		Key              string                 `gorm:"column:key"`
		ValueEncrypted   []byte                 `gorm:"column:value_encrypted"`
		PolicyAttributes map[string]interface{} `gorm:"type:jsonb;serializer:json;column:policy_attributes"`
		IndexedContent   map[string]string      `gorm:"type:jsonb;serializer:json;column:indexed_content"`
		MemoryKind       string                 `gorm:"column:memory_kind"`
		Revision         int64                  `gorm:"column:revision"`
		CreatedAt        time.Time              `gorm:"column:created_at"`
	}
	var rows []scanRow
	if err := db.Raw("SELECT id, namespace, key, value_encrypted, policy_attributes, indexed_content, memory_kind, revision, created_at FROM memories WHERE "+whereClause+" ORDER BY created_at ASC, id ASC LIMIT ?", append(args, limit)...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]registryepisodic.MigrationCandidate, 0, len(rows))
	for _, r := range rows {
		out = append(out, registryepisodic.MigrationCandidate{
			ID:               r.ID,
			CreatedAt:        r.CreatedAt,
			Namespace:        r.Namespace,
			Key:              r.Key,
			ValueEncrypted:   r.ValueEncrypted,
			PolicyAttributes: r.PolicyAttributes,
			IndexedContent:   r.IndexedContent,
			MemoryKind:       r.MemoryKind,
			Revision:         r.Revision,
		})
	}
	return out, nil
}

// MigrateOneMemoryKindCAS atomically updates attributes and schema on a memory row only if
// the expected schema and revision still match. Clears indexed_at to trigger re-indexing.
// Uses writeDBFor so it participates in the enclosing InWriteTx transaction.
func (e *postgresEpisodicStore) MigrateOneMemoryKindCAS(ctx context.Context, id uuid.UUID, expectedKind string, expectedRevision int64, newAttributes map[string]interface{}, newSchema string) error {
	db, err := e.s.writeDBFor(ctx, "MigrateOneMemoryKindCAS")
	if err != nil {
		return err
	}
	attrJSON, err := jsonMarshal(newAttributes)
	if err != nil {
		return err
	}
	schemaCondition := "memory_kind = ?"
	args := []interface{}{attrJSON, newSchema, id, expectedRevision, expectedKind}
	res := db.Exec(
		"UPDATE memories SET policy_attributes = ?::jsonb, memory_kind = ?, indexed_at = NULL, revision = revision + 1 WHERE id = ? AND revision = ? AND "+schemaCondition,
		args...,
	)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return registryepisodic.ErrMemoryRevisionConflict
	}
	return nil
}

func jsonMarshal(v interface{}) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// CountMemoriesPendingIndexByKind returns the number of memory rows with the given
// targetKind and indexed_at IS NULL, optionally scoped to a namespace prefix.
// Defect 10 fix: used for accurate race-safe vector_pending_count at read time.
// Uses dbFor so it can participate in a read or write transaction scope.
func (e *postgresEpisodicStore) CountMemoriesPendingIndexByKind(ctx context.Context, targetKind string, namespacePrefix []string) (int64, error) {
	db := e.s.dbFor(ctx)
	schemaWhere := "memory_kind = ?"
	args := []interface{}{targetKind}
	whereClause := schemaWhere + " AND indexed_at IS NULL"
	if len(namespacePrefix) > 0 {
		encoded, err := episodic.EncodeNamespace(namespacePrefix, 0)
		if err != nil {
			return 0, err
		}
		whereClause += " AND (namespace = ? OR namespace LIKE ?)"
		args = append(args, encoded, episodic.NamespacePrefixPattern(encoded))
	}
	var count int64
	err := db.Raw("SELECT COUNT(*) FROM memories WHERE "+whereClause, args...).Scan(&count).Error
	return count, err
}
