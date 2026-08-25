package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const pgSchemaPath = "db/schema.sql"

// TestPostgresSchemaMemoryKindNotNull verifies the fresh-only invariant is encoded
// in the Postgres schema: memories.memory_kind must be NOT NULL.
func TestPostgresSchemaMemoryKindNotNull(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile(pgSchemaPath)
	require.NoError(t, err, "reading postgres schema.sql")
	schema := string(src)

	assert.Contains(t, schema, "memory_kind",
		"memories table must have memory_kind column in Postgres schema")

	// The column definition must have NOT NULL.
	assert.Contains(t, schema, "memory_kind       TEXT        NOT NULL",
		"memories.memory_kind must be NOT NULL in Postgres schema")
}

// TestPostgresSchemaKindVersionsDefinedBeforeMemoriesDefault verifies that the
// referenced versions table is defined before the memories table. The migrator
// seeds the Go-embedded default/v1 definition after executing this DDL.
func TestPostgresSchemaKindVersionsDefinedBeforeMemoriesDefault(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile(pgSchemaPath)
	require.NoError(t, err, "reading postgres schema.sql")
	schema := string(src)

	versionsIdx := strings.Index(schema, "CREATE TABLE IF NOT EXISTS memory_kind_versions")
	// memories table definition with the memory_kind column DEFAULT.
	memoriesTableIdx := strings.Index(schema, "CREATE TABLE IF NOT EXISTS memories")

	require.True(t, versionsIdx >= 0, "memory_kind_versions table must be present")
	require.True(t, memoriesTableIdx >= 0, "CREATE TABLE memories must be present")

	assert.Less(t, versionsIdx, memoriesTableIdx,
		"memory_kind_versions must be defined before the memories foreign key")
}

// TestPostgresSchemaMemoryKindIndex verifies the composite index on memories for
// efficient kind-filtered reads and migration scans.
func TestPostgresSchemaMemoryKindIndex(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile(pgSchemaPath)
	require.NoError(t, err)
	assert.Contains(t, string(src), "ON memories (memory_kind, created_at, id)",
		"migration cursor index must match (memory_kind, created_at, id) ordering")
}

// TestPgvectorSchemaMemoryKindNotNull verifies the pgvector schema requires
// memory_kind to be NOT NULL on vector rows.
func TestPgvectorSchemaMemoryKindNotNull(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("../pgvector/db/pgvector-schema.sql")
	if os.IsNotExist(err) {
		// pgvector schema lives in the vector plugin directory.
		src, err = os.ReadFile("../../../plugin/vector/pgvector/db/pgvector-schema.sql")
	}
	require.NoError(t, err, "reading pgvector-schema.sql")
	assert.Contains(t, string(src), "memory_kind       TEXT NOT NULL",
		"memory_vectors.memory_kind must be NOT NULL in pgvector schema")
	assert.Contains(t, string(src), "memory_revision   BIGINT NOT NULL CHECK (memory_revision > 0)",
		"memory_vectors.memory_revision must be unambiguously positive")
}

// TestNoBaseHandleInEpisodicStore is a static regression guard: the episodic store
// implementation files must not use e.db.WithContext directly. All queries must go
// through e.s.dbFor (reads) or e.s.writeDBFor (writes) so they participate in the
// caller's scoped transaction. A base-handle bypass silently breaks read/write
// isolation, transaction sharing, and test isolation.
func TestNoBaseHandleInEpisodicStore(t *testing.T) {
	t.Parallel()
	files := []string{
		"episodic_store.go",
		"episodic_admin_stats.go",
		"episodic_kind.go",
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		require.NoError(t, err, "reading %s", f)
		content := string(src)
		// The only legal uses of e.db are struct literal assignments and the
		// store struct definition itself (e.g. "db: db", "db *gorm.DB").
		// Direct method calls like e.db.WithContext(...) or e.db.Exec(...)
		// bypass the scoped transaction and are forbidden in episodic methods.
		assert.NotContains(t, content, "e.db.WithContext(",
			"%s must not use e.db.WithContext — use e.s.dbFor or e.s.writeDBFor instead", f)
		assert.NotContains(t, content, "e.db.Exec(",
			"%s must not use e.db.Exec — use e.s.writeDBFor instead", f)
		assert.NotContains(t, content, "e.db.Raw(",
			"%s must not use e.db.Raw — use e.s.dbFor or e.s.writeDBFor instead", f)
		assert.NotContains(t, content, "e.db.Table(",
			"%s must not use e.db.Table — use e.s.dbFor or e.s.writeDBFor instead", f)
		assert.NotContains(t, content, "e.db.Where(",
			"%s must not use e.db.Where — use e.s.dbFor or e.s.writeDBFor instead", f)
	}
}
