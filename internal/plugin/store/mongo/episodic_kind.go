//go:build !nomongo

package mongo

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/chirino/memory-service/internal/episodic"
	"github.com/chirino/memory-service/internal/model"
	registryepisodic "github.com/chirino/memory-service/internal/registry/episodic"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func (s *mongoEpisodicStore) db() *mongo.Database {
	return s.col.Database()
}

func (s *mongoEpisodicStore) schemaVersions() *mongo.Collection {
	return s.db().Collection("memory_kind_versions")
}

func (s *mongoEpisodicStore) schemaMigrations() *mongo.Collection {
	return s.db().Collection("memory_kind_migrations")
}

// CreateMemoryKindVersion persists an immutable schema version.
// Defect 11 fix: translate duplicate-key races to the correct sentinel errors
// (ErrMemoryKindVersionConflict if content differs, otherwise return existing).
func (s *mongoEpisodicStore) CreateMemoryKindVersion(ctx context.Context, version model.MemoryKindVersion) (*model.MemoryKindVersion, error) {
	col := s.schemaVersions()
	var existingDoc bson.M
	err := col.FindOne(ctx, bson.M{"_id": version.Name}).Decode(&existingDoc)
	if err == nil {
		existing := schemaVersionFromDoc(existingDoc)
		if !schemaVersionsEqual(existing, version) {
			return nil, registryepisodic.ErrMemoryKindVersionConflict
		}
		return &existing, nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, err
	}
	doc := schemaVersionToDoc(version)
	_, err = col.InsertOne(ctx, doc)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			// Concurrent race: reload and check idempotency.
			var racedDoc bson.M
			if rErr := col.FindOne(ctx, bson.M{"_id": version.Name}).Decode(&racedDoc); rErr != nil {
				if errors.Is(rErr, mongo.ErrNoDocuments) {
					// Should not happen, but treat as conflict.
					return nil, registryepisodic.ErrMemoryKindVersionConflict
				}
				return nil, rErr
			}
			existing := schemaVersionFromDoc(racedDoc)
			if !schemaVersionsEqual(existing, version) {
				return nil, registryepisodic.ErrMemoryKindVersionConflict
			}
			return &existing, nil
		}
		return nil, err
	}
	return &version, nil
}

// GetMemoryKindVersion retrieves a schema version by canonical name.
func (s *mongoEpisodicStore) GetMemoryKindVersion(ctx context.Context, name string) (*model.MemoryKindVersion, error) {
	col := s.schemaVersions()
	var doc bson.M
	err := col.FindOne(ctx, bson.M{"_id": name}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v := schemaVersionFromDoc(doc)
	return &v, nil
}

// ListMemoryKindVersions lists schema versions, optionally filtered by family.
func (s *mongoEpisodicStore) ListMemoryKindVersions(ctx context.Context, family string) ([]model.MemoryKindVersion, error) {
	col := s.schemaVersions()
	filter := bson.M{}
	if family != "" {
		filter = bson.M{"family": family}
	}
	cursor, err := col.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx) //nolint:errcheck
	var versions []model.MemoryKindVersion
	for cursor.Next(ctx) {
		var doc bson.M
		if err := cursor.Decode(&doc); err != nil {
			return nil, err
		}
		versions = append(versions, schemaVersionFromDoc(doc))
	}
	return versions, cursor.Err()
}

// CreateMemoryKindMigration persists a new migration.
// Defect 11 fix: translate duplicate-key race to ErrMemoryKindMigrationActiveForSource.
func (s *mongoEpisodicStore) CreateMemoryKindMigration(ctx context.Context, m model.MemoryKindMigration) (*model.MemoryKindMigration, error) {
	col := s.schemaMigrations()
	// Check for active migration.
	var existing bson.M
	err := col.FindOne(ctx, bson.M{
		"source": m.Source,
		"state":  bson.M{"$in": bson.A{"queued", "running", "canceling"}},
	}).Decode(&existing)
	if err == nil {
		return nil, registryepisodic.ErrMemoryKindMigrationActiveForSource
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, err
	}
	doc := migrationToDoc(m)
	_, err = col.InsertOne(ctx, doc)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			// Concurrent race on the unique ID; treat as active-for-source.
			return nil, registryepisodic.ErrMemoryKindMigrationActiveForSource
		}
		return nil, err
	}
	return &m, nil
}

func (s *mongoEpisodicStore) CreateMemoryKindMigrationAndTask(ctx context.Context, m model.MemoryKindMigration) (*model.MemoryKindMigration, error) {
	created, err := s.CreateMemoryKindMigration(ctx, m)
	if err != nil {
		return nil, err
	}
	taskName := "migration:" + created.ID.String()
	now := time.Now().UTC()
	_, err = s.db().Collection("tasks").InsertOne(ctx, bson.M{
		"_id": uuid.New().String(), "task_name": taskName, "task_type": "memory_kind_migration",
		"task_body":  bson.M{"migration_id": created.ID.String(), "taskName": taskName},
		"created_at": now, "retry_at": now, "processing_at": nil, "retry_count": 0,
	})
	if err != nil {
		return nil, fmt.Errorf("create initial migration task: %w", err)
	}
	return created, nil
}

func (s *mongoEpisodicStore) CreateMemoryKindMigrationTask(ctx context.Context, body map[string]interface{}) error {
	now := time.Now().UTC()
	doc := bson.M{"_id": uuid.New().String(), "task_type": "memory_kind_migration", "task_body": body, "created_at": now, "retry_at": now, "processing_at": nil, "retry_count": 0}
	if raw, ok := body["taskName"].(string); ok && strings.TrimSpace(raw) != "" {
		doc["task_name"] = strings.TrimSpace(raw)
	}
	_, err := s.db().Collection("tasks").InsertOne(ctx, doc)
	if doc["task_name"] != nil && mongo.IsDuplicateKeyError(err) {
		return nil
	}
	return err
}

// DeleteMemoryKindMigration permanently removes a migration record by UUID.
func (s *mongoEpisodicStore) DeleteMemoryKindMigration(ctx context.Context, id uuid.UUID) error {
	col := s.schemaMigrations()
	_, err := col.DeleteOne(ctx, bson.M{"_id": id.String()})
	return err
}

// GetMemoryKindMigration retrieves a migration by UUID.
func (s *mongoEpisodicStore) GetMemoryKindMigration(ctx context.Context, id uuid.UUID) (*model.MemoryKindMigration, error) {
	col := s.schemaMigrations()
	var doc bson.M
	err := col.FindOne(ctx, bson.M{"_id": id.String()}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m := migrationFromDoc(doc)
	return &m, nil
}

// ListMemoryKindMigrations lists migrations, optionally filtered by state.
func (s *mongoEpisodicStore) ListMemoryKindMigrations(ctx context.Context, state string) ([]model.MemoryKindMigration, error) {
	col := s.schemaMigrations()
	filter := bson.M{}
	if state != "" {
		filter = bson.M{"state": state}
	}
	cursor, err := col.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx) //nolint:errcheck
	var migrations []model.MemoryKindMigration
	for cursor.Next(ctx) {
		var doc bson.M
		if err := cursor.Decode(&doc); err != nil {
			return nil, err
		}
		migrations = append(migrations, migrationFromDoc(doc))
	}
	return migrations, cursor.Err()
}

// UpdateMemoryKindMigrationCancelRequested marks a queued or running migration as canceling.
// Returns ErrMemoryKindMigrationNotFound if no migration with the given ID exists.
func (s *mongoEpisodicStore) UpdateMemoryKindMigrationCancelRequested(ctx context.Context, id uuid.UUID) error {
	col := s.schemaMigrations()
	res, err := col.UpdateOne(
		ctx,
		bson.M{"_id": id.String(), "state": bson.M{"$in": bson.A{"queued", "running"}}},
		bson.M{"$set": bson.M{"cancel_requested": true, "state": "canceling"}},
	)
	if err != nil {
		return err
	}
	if res.MatchedCount > 0 {
		return nil
	}
	// No rows matched: either ID doesn't exist or migration is already in a terminal/canceling state.
	m, err := s.GetMemoryKindMigration(ctx, id)
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
func (s *mongoEpisodicStore) UpdateMemoryKindMigrationState(ctx context.Context, id uuid.UUID, state string, startedAt, completedAt *time.Time) error {
	col := s.schemaMigrations()
	set := bson.M{"state": state}
	if startedAt != nil {
		set["started_at"] = *startedAt
	}
	if completedAt != nil {
		set["completed_at"] = *completedAt
	}
	filter := bson.M{"_id": id.String()}
	switch state {
	case model.MigrationStateRunning:
		filter["state"] = model.MigrationStateQueued
		filter["cancel_requested"] = false
	case model.MigrationStateCanceled:
		filter["state"] = bson.M{"$in": bson.A{model.MigrationStateQueued, model.MigrationStateRunning, model.MigrationStateCanceling}}
		filter["cancel_requested"] = true
	}
	res, err := col.UpdateOne(ctx, filter, bson.M{"$set": set})
	if err != nil {
		return err
	}
	if res.MatchedCount != 1 {
		return registryepisodic.ErrMemoryKindMigrationStateConflict
	}
	return nil
}

// UpdateMemoryKindMigrationIncrementMigrated increments migrated_count by 1.
// The caller combines this update with the memory CAS inside the episodic Mongo
// session transaction opened by InWriteTx.
func (s *mongoEpisodicStore) UpdateMemoryKindMigrationIncrementMigrated(ctx context.Context, id uuid.UUID) error {
	col := s.schemaMigrations()
	res, err := col.UpdateOne(
		ctx,
		bson.M{"_id": id.String(), "state": model.MigrationStateRunning, "cancel_requested": false},
		bson.M{"$inc": bson.M{"migrated_count": 1}},
	)
	if err != nil {
		return err
	}
	if res.MatchedCount != 1 {
		return registryepisodic.ErrMemoryKindMigrationStateConflict
	}
	return nil
}

// UpdateMemoryKindMigrationSucceeded atomically sets state=succeeded, completed_at,
// and the ABSOLUTE skipped_tombstone_count.  Idempotent: $set overwrites on retry.
func (s *mongoEpisodicStore) UpdateMemoryKindMigrationSucceeded(ctx context.Context, id uuid.UUID, completedAt time.Time, absoluteSkippedTombstoneCount int64) error {
	col := s.schemaMigrations()
	_, err := col.UpdateOne(
		ctx,
		bson.M{"_id": id.String()},
		bson.M{"$set": bson.M{
			"state":                   "succeeded",
			"completed_at":            completedAt,
			"skipped_tombstone_count": absoluteSkippedTombstoneCount,
		}},
	)
	return err
}

// UpdateMemoryKindMigrationCounters increments progress counters (used by indexer path).
func (s *mongoEpisodicStore) UpdateMemoryKindMigrationCounters(ctx context.Context, id uuid.UUID, migratedDelta, tombstoneDelta, vectorPendingDelta int64) error {
	col := s.schemaMigrations()
	_, err := col.UpdateOne(
		ctx,
		bson.M{"_id": id.String()},
		bson.M{"$inc": bson.M{
			"migrated_count":          migratedDelta,
			"skipped_tombstone_count": tombstoneDelta,
			"vector_pending_count":    vectorPendingDelta,
		}},
	)
	return err
}

// UpdateMemoryKindMigrationStateFailed atomically sets state=failed, last_error_code, and completed_at.
func (s *mongoEpisodicStore) UpdateMemoryKindMigrationStateFailed(ctx context.Context, id uuid.UUID, errorCode string, completedAt time.Time) error {
	col := s.schemaMigrations()
	_, err := col.UpdateOne(
		ctx,
		bson.M{"_id": id.String()},
		bson.M{"$set": bson.M{
			"state":           "failed",
			"last_error_code": errorCode,
			"completed_at":    completedAt,
		}},
	)
	return err
}

// UpdateMemoryKindMigrationRetry increments retry_count and sets last_error_code.
func (s *mongoEpisodicStore) UpdateMemoryKindMigrationRetry(ctx context.Context, id uuid.UUID, errorCode string) error {
	col := s.schemaMigrations()
	_, err := col.UpdateOne(
		ctx,
		bson.M{"_id": id.String()},
		bson.M{
			"$inc": bson.M{"retry_count": 1},
			"$set": bson.M{"last_error_code": errorCode},
		},
	)
	return err
}

// ResolveKindForWrite resolves the canonical schema name for a write.
func (s *mongoEpisodicStore) ResolveKindForWrite(ctx context.Context, sel string) (string, error) {
	canonicalName := strings.TrimSpace(sel)
	if canonicalName == "" {
		canonicalName = episodic.DefaultKindName
	}
	if _, _, err := episodic.ParseCanonicalKindName(canonicalName); err != nil {
		return "", fmt.Errorf("%w: %v", registryepisodic.ErrMemoryKindInvalid, err)
	}
	v, err := s.GetMemoryKindVersion(ctx, canonicalName)
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

// SetMemoryKindIndexedAtCAS sets indexed_at only if schema and revision still match.
// archived_at is intentionally not guarded — see postgres implementation for rationale.
func (s *mongoEpisodicStore) SetMemoryKindIndexedAtCAS(ctx context.Context, memoryID uuid.UUID, expectedKind string, expectedRevision int64, indexedAt time.Time) error {
	col := s.col
	filter := bson.M{
		"_id":         memoryID.String(),
		"revision":    expectedRevision,
		"memory_kind": expectedKind,
	}
	result, err := col.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"indexed_at": indexedAt}})
	if err != nil {
		return err
	}
	if result.MatchedCount == 0 {
		return registryepisodic.ErrMemoryRevisionConflict
	}
	return nil
}

// --- helpers ---

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
	aRego, bRego := "", ""
	if a.AttributesRego != nil {
		aRego = *a.AttributesRego
	}
	if b.AttributesRego != nil {
		bRego = *b.AttributesRego
	}
	return aRego == bRego
}

func schemaVersionToDoc(v model.MemoryKindVersion) bson.M {
	family := ""
	if idx := indexOfSlash(v.Name); idx >= 0 {
		family = v.Name[:idx]
	}
	doc := bson.M{
		"_id":             v.Name,
		"family":          family,
		"attribute_types": v.AttributeTypes,
		"writable":        v.Writable,
		"created_at":      v.CreatedAt,
	}
	if v.AttributesRego != nil {
		doc["attributes_rego"] = *v.AttributesRego
	}
	return doc
}

func indexOfSlash(s string) int {
	for i, c := range s {
		if c == '/' {
			return i
		}
	}
	return -1
}

func schemaVersionFromDoc(doc bson.M) model.MemoryKindVersion {
	v := model.MemoryKindVersion{
		Name:     docString(doc, "_id"),
		Writable: docBool(doc, "writable"),
	}
	// attribute_types may be stored as bson.M (map) or bson.D (ordered slice)
	// depending on the driver version and how the document was decoded.
	switch raw := doc["attribute_types"].(type) {
	case bson.M:
		v.AttributeTypes = make(map[string]string, len(raw))
		for k, val := range raw {
			if s, ok := val.(string); ok {
				v.AttributeTypes[k] = s
			}
		}
	case bson.D:
		v.AttributeTypes = make(map[string]string, len(raw))
		for _, elem := range raw {
			if s, ok := elem.Value.(string); ok {
				v.AttributeTypes[elem.Key] = s
			}
		}
	}
	if rego, ok := doc["attributes_rego"].(string); ok && rego != "" {
		v.AttributesRego = &rego
	}
	v.CreatedAt = docTime(doc, "created_at")
	return v
}

func migrationToDoc(m model.MemoryKindMigration) bson.M {
	doc := bson.M{
		"_id":                     m.ID.String(),
		"source":                  m.Source,
		"target":                  m.Target,
		"state":                   m.State,
		"cancel_requested":        m.CancelRequested,
		"migrated_count":          m.MigratedCount,
		"skipped_tombstone_count": m.SkippedTombstoneCount,
		"vector_pending_count":    m.VectorPendingCount,
		"retry_count":             m.RetryCount,
		"created_at":              m.CreatedAt,
	}
	if len(m.NamespacePrefix) > 0 {
		doc["namespace_prefix"] = m.NamespacePrefix
	}
	if m.LastErrorCode != nil {
		doc["last_error_code"] = *m.LastErrorCode
	}
	if m.StartedAt != nil {
		doc["started_at"] = *m.StartedAt
	}
	if m.CompletedAt != nil {
		doc["completed_at"] = *m.CompletedAt
	}
	return doc
}

func migrationFromDoc(doc bson.M) model.MemoryKindMigration {
	idStr := docString(doc, "_id")
	id, _ := uuid.Parse(idStr)
	m := model.MemoryKindMigration{
		ID:                    id,
		Source:                docString(doc, "source"),
		Target:                docString(doc, "target"),
		State:                 docString(doc, "state"),
		CancelRequested:       docBool(doc, "cancel_requested"),
		MigratedCount:         docInt64(doc, "migrated_count"),
		SkippedTombstoneCount: docInt64(doc, "skipped_tombstone_count"),
		VectorPendingCount:    docInt64(doc, "vector_pending_count"),
		RetryCount:            int(docInt64(doc, "retry_count")),
	}
	if raw, ok := doc["namespace_prefix"].(bson.A); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok {
				m.NamespacePrefix = append(m.NamespacePrefix, s)
			}
		}
	}
	if errCode, ok := doc["last_error_code"].(string); ok && errCode != "" {
		m.LastErrorCode = &errCode
	}
	m.CreatedAt = docTime(doc, "created_at")
	if t := docTimePtr(doc, "started_at"); t != nil {
		m.StartedAt = t
	}
	if t := docTimePtr(doc, "completed_at"); t != nil {
		m.CompletedAt = t
	}
	return m
}

func docString(doc bson.M, key string) string {
	v, _ := doc[key].(string)
	return v
}

func docBool(doc bson.M, key string) bool {
	v, _ := doc[key].(bool)
	return v
}

// docTime decodes a document time field that may be stored as time.Time (Go
// driver decoded) or bson.DateTime (raw wire format, milliseconds since epoch).
func docTime(doc bson.M, key string) time.Time {
	switch v := doc[key].(type) {
	case time.Time:
		return v.UTC()
	case bson.DateTime:
		return v.Time().UTC()
	}
	return time.Time{}
}

// docTimePtr is like docTime but returns nil when the field is absent or zero.
func docTimePtr(doc bson.M, key string) *time.Time {
	switch v := doc[key].(type) {
	case time.Time:
		if v.IsZero() {
			return nil
		}
		t := v.UTC()
		return &t
	case bson.DateTime:
		t := v.Time().UTC()
		return &t
	}
	return nil
}

func docInt64(doc bson.M, key string) int64 {
	switch v := doc[key].(type) {
	case int64:
		return v
	case int32:
		return int64(v)
	case float64:
		return int64(v)
	}
	return 0
}

// FindMemoriesToMigrateByKind returns up to limit memory rows whose effective schema is sourceKind.
func (s *mongoEpisodicStore) FindMemoriesToMigrateByKind(ctx context.Context, sourceKind string, namespacePrefix []string, afterCreatedAt time.Time, afterID uuid.UUID, limit int) ([]registryepisodic.MigrationCandidate, error) {
	col := s.col
	schemaFilter := bson.M{"memory_kind": sourceKind}
	filter := bson.M{
		"$and": bson.A{
			schemaFilter,
			bson.M{"$or": bson.A{
				bson.M{"created_at": bson.M{"$gt": afterCreatedAt}},
				bson.M{"created_at": afterCreatedAt, "_id": bson.M{"$gt": afterID.String()}},
			}},
		},
	}
	if len(namespacePrefix) > 0 {
		encoded, err := encodeNS(namespacePrefix)
		if err != nil {
			return nil, err
		}
		escaped := regexp.QuoteMeta(encoded)
		// Match the exact encoded namespace OR any descendant (separated by RS \x1e).
		// The bare prefix regex "^encoded" would also match siblings like ["user","alice"]
		// when the prefix is ["user","a"].
		filter["$and"] = append(filter["$and"].(bson.A),
			bson.M{"$or": bson.A{
				bson.M{"namespace": encoded},
				bson.M{"namespace": bson.M{"$regex": "^" + escaped + "\x1e"}},
			}},
		)
	}
	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}, {Key: "_id", Value: 1}}).SetLimit(int64(limit))
	cursor, err := col.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var out []registryepisodic.MigrationCandidate
	for cursor.Next(ctx) {
		var doc memoryDoc
		if err := cursor.Decode(&doc); err != nil {
			return nil, err
		}
		id, err := uuid.Parse(doc.ID)
		if err != nil {
			continue
		}
		out = append(out, registryepisodic.MigrationCandidate{
			ID:               id,
			CreatedAt:        doc.CreatedAt,
			Namespace:        doc.Namespace,
			Key:              doc.Key,
			ValueEncrypted:   doc.ValueEncrypted,
			PolicyAttributes: doc.PolicyAttributes,
			IndexedContent:   doc.IndexedContent,
			MemoryKind:       doc.MemoryKind,
			Revision:         doc.Revision,
		})
	}
	return out, cursor.Err()
}

// MigrateOneMemoryKindCAS atomically updates attributes and schema on a memory document
// only if the expected schema and revision still match. Clears indexed_at.
func (s *mongoEpisodicStore) MigrateOneMemoryKindCAS(ctx context.Context, id uuid.UUID, expectedKind string, expectedRevision int64, newAttributes map[string]interface{}, newSchema string) error {
	col := s.col
	schemaFilter := bson.M{"memory_kind": expectedKind}
	filter := bson.M{
		"$and": bson.A{
			bson.M{"_id": id.String()},
			bson.M{"revision": expectedRevision},
			schemaFilter,
		},
	}
	update := bson.M{
		"$set": bson.M{
			"policy_attributes": newAttributes,
			"memory_kind":       newSchema,
		},
		// Clear indexed_at so the episodic indexer re-syncs vector metadata after cutover.
		// Use $unset rather than $set to null to keep document shape consistent with
		// FindMemoriesPendingIndexing which checks {"indexed_at": {"$exists": false}}.
		"$unset": bson.M{
			"indexed_at": "",
		},
		"$inc": bson.M{"revision": int64(1)},
	}
	result, err := col.UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}
	if result.MatchedCount == 0 {
		return registryepisodic.ErrMemoryRevisionConflict
	}
	return nil
}

// CountMemoriesPendingIndexByKind returns the number of memory rows with the given
// targetKind and indexed_at absent/null, optionally scoped to a namespace prefix.
// Defect 10 fix: used for accurate race-safe vector_pending_count at read time.
func (s *mongoEpisodicStore) CountMemoriesPendingIndexByKind(ctx context.Context, targetKind string, namespacePrefix []string) (int64, error) {
	col := s.col
	schemaFilter := bson.M{"memory_kind": targetKind}
	// indexed_at absent or null means pending re-index.
	pendingFilter := bson.M{"$or": bson.A{
		bson.M{"indexed_at": bson.M{"$exists": false}},
		bson.M{"indexed_at": nil},
	}}
	filter := bson.M{"$and": bson.A{schemaFilter, pendingFilter}}
	if len(namespacePrefix) > 0 {
		encoded, err := encodeNS(namespacePrefix)
		if err != nil {
			return 0, err
		}
		escaped := regexp.QuoteMeta(encoded)
		filter["$and"] = append(filter["$and"].(bson.A),
			bson.M{"$or": bson.A{
				bson.M{"namespace": encoded},
				bson.M{"namespace": bson.M{"$regex": "^" + escaped + "\x1e"}},
			}},
		)
	}
	return col.CountDocuments(ctx, filter)
}
