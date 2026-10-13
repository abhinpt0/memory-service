//go:build cgo

package sqlitecodec

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTimestampSQLBoundary(t *testing.T) {
	db, err := sql.Open(DriverName, ":memory:")
	require.NoError(t, err)
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE timestamps(id INTEGER PRIMARY KEY,created_at INTEGER_SECONDS NOT NULL DEFAULT (unixepoch()),created_at_ns INTEGER NOT NULL DEFAULT 0,expires_at INTEGER_SECONDS,expires_at_ns INTEGER)`)
	require.NoError(t, err)
	instants := []time.Time{
		time.Date(1, 1, 1, 0, 0, 0, 123456789, time.UTC), time.Unix(-1, 999999999),
		time.Date(2026, 10, 13, 10, 20, 30, 123456789, time.FixedZone("east", 19800)),
		time.Date(2280, 4, 17, 2, 13, 20, 123456789, time.UTC), time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
	}
	for _, prepared := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "prepared"}[prepared], func(t *testing.T) {
			for i, instant := range instants {
				id := i + 1
				if prepared {
					id += len(instants)
				}
				insert := `INSERT INTO timestamps(id,created_at,created_at_ns,expires_at,expires_at_ns) VALUES (?,?,?,?,?)`
				if prepared {
					stmt, e := db.PrepareContext(context.Background(), insert)
					require.NoError(t, e)
					_, e = stmt.Exec(id, instant, &instant)
					require.NoError(t, e)
					require.NoError(t, stmt.Close())
				} else {
					_, e := db.Exec(insert, id, instant, &instant)
					require.NoError(t, e)
				}
				var stored time.Time
				var expires *time.Time
				var sec, ns int64
				var storage string
				query := `SELECT created_at,created_at_ns,expires_at,expires_at_ns,CAST(created_at AS INTEGER) AS raw_seconds,CAST(created_at_ns AS INTEGER) AS raw_nanos,typeof(created_at) FROM timestamps WHERE id=? AND (created_at,created_at_ns)=(?,?)`
				if prepared {
					stmt, e := db.Prepare(query)
					require.NoError(t, e)
					require.NoError(t, stmt.QueryRow(id, instant.UTC()).Scan(&stored, &expires, &sec, &ns, &storage))
					require.NoError(t, stmt.Close())
				} else {
					require.NoError(t, db.QueryRow(query, id, instant.UTC()).Scan(&stored, &expires, &sec, &ns, &storage))
				}
				require.True(t, instant.Equal(stored))
				require.Equal(t, time.UTC, stored.Location())
				require.True(t, instant.Equal(*expires))
				require.Equal(t, instant.Unix(), sec)
				require.Equal(t, int64(instant.Nanosecond()), ns)
				require.Equal(t, "integer", storage)
			}
			query := `SELECT ? + ?`
			if prepared {
				stmt, e := db.Prepare(query)
				require.NoError(t, e)
				defer stmt.Close()
				require.Error(t, stmt.QueryRow(1).Scan(new(int)))
				require.Error(t, stmt.QueryRow(1, 2, 3).Scan(new(int)))
			} else {
				require.Error(t, db.QueryRow(query, 1).Scan(new(int)))
				require.Error(t, db.QueryRow(query, 1, 2, 3).Scan(new(int)))
			}
		})
	}
	// Parameterized statements after the first batch statement are unsupported.
	_, err = db.Exec(`SELECT 1; SELECT ?`, 1)
	require.ErrorContains(t, err, "expects 0 parameters, got 1")
	require.ErrorContains(t, err, "first statement")
	require.ErrorContains(t, db.QueryRow(`SELECT :instant`, sql.Named("instant", instants[0])).Scan(new(int)), "positional parameters")
	var oldest time.Time
	require.NoError(t, db.QueryRow(`SELECT created_at AS oldest_at,created_at_ns AS oldest_at_ns FROM timestamps ORDER BY created_at,created_at_ns LIMIT 1`).Scan(&oldest))
	require.True(t, instants[0].Equal(oldest))

	beforeDefault := time.Now().Truncate(time.Second)
	_, err = db.Exec(`INSERT INTO timestamps(id) VALUES(100)`)
	require.NoError(t, err)
	var defaultTime time.Time
	var nullable *time.Time
	require.NoError(t, db.QueryRow(`SELECT created_at,created_at_ns,expires_at,expires_at_ns FROM timestamps WHERE id=100`).Scan(&defaultTime, &nullable))
	require.False(t, defaultTime.Before(beforeDefault))
	require.False(t, defaultTime.After(time.Now()))
	require.Zero(t, defaultTime.Nanosecond())
	require.Nil(t, nullable)
}

func TestLegacyTimestampPrecision(t *testing.T) {
	for _, input := range []string{"2026-01-02 03:04:05.123456789-07:00", "2026-01-02T15:34:05.123456789+05:30", "2026-01-02T10:04:05.123456789Z"} {
		instant, err := legacyTime(input)
		require.NoError(t, err)
		require.True(t, time.Date(2026, 1, 2, 10, 4, 5, 123456789, time.UTC).Equal(*instant))
	}
	for _, input := range []any{nil, []byte(nil)} {
		result, err := legacyTime(input)
		require.NoError(t, err)
		require.Nil(t, result)
	}
	_, err := legacyTime("invalid")
	require.ErrorContains(t, err, "invalid legacy SQLite timestamp")
}
