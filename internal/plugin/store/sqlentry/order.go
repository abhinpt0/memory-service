package sqlentry

import (
	"fmt"
	"strings"
	"time"

	"github.com/chirino/memory-service/internal/model"
	"gorm.io/gorm"
)

func entryColumn(alias, name string) string {
	if alias == "" {
		return name
	}
	return alias + "." + name
}

// BACKWARD COMPATIBILITY: remove in a future breaking release.
// Legacy SQLite entries retain nanoseconds within a millisecond. Extract only
// their fractional seconds, independent of the timestamp's timezone offset.
func sqliteEntryFractionSQL(alias string) string {
	col := entryColumn(alias, "created_at")
	return fmt.Sprintf("CASE WHEN substr(%s, 20, 1) = '.' THEN CAST(round(CAST('0.' || substr(%s, 21) AS REAL) * 1000000000) AS INTEGER) ELSE 0 END", col, col)
}

// SQLiteEntryTimeSQL is a numeric (milliseconds, sub-millisecond nanoseconds)
// key. Keep both parts so upgrades preserve the ordering of legacy entries.
func SQLiteEntryTimeSQL(alias string) string {
	return fmt.Sprintf("(%s, (%s) %% 1000000)", entryColumn(alias, "created_at_unix_ms"), sqliteEntryFractionSQL(alias))
}

func EntryTimeOrderSQL(db *gorm.DB, alias string, descending bool) string {
	direction := "ASC"
	if descending {
		direction = "DESC"
	}
	columns := []string{entryColumn(alias, "created_at")}
	if db.Dialector.Name() == "sqlite" {
		columns = []string{entryColumn(alias, "created_at_unix_ms"), "(" + sqliteEntryFractionSQL(alias) + ") % 1000000"}
	}
	for i := range columns {
		columns[i] += " " + direction
	}
	return strings.Join(columns, ", ")
}

func EntryOrderSQL(db *gorm.DB, alias string, descending bool) string {
	direction, nulls := "ASC", "FIRST"
	if descending {
		direction, nulls = "DESC", "LAST"
	}
	return EntryTimeOrderSQL(db, alias, descending) + fmt.Sprintf(", %s %s NULLS %s, %s %s", entryColumn(alias, "seq"), direction, nulls, entryColumn(alias, "id"), direction)
}

func entryTimeBound(db *gorm.DB, alias string, bound model.Entry) (string, string, []any) {
	if db.Dialector.Name() == "sqlite" {
		return SQLiteEntryTimeSQL(alias), "(?, ?)", []any{bound.CreatedAt.UnixMilli(), bound.CreatedAt.Nanosecond() % 1000000}
	}
	return entryColumn(alias, "created_at"), "?", []any{bound.CreatedAt}
}

func WhereEntryTimeAfterAlias(tx *gorm.DB, alias string, bound time.Time, id any) *gorm.DB {
	if tx.Dialector.Name() == "sqlite" {
		tx = tx.Where(entryColumn(alias, "created_at_unix_ms")+" >= ?", bound.UnixMilli())
	}
	key, placeholder, values := entryTimeBound(tx, alias, model.Entry{CreatedAt: bound})
	args := append(append([]any(nil), values...), values...)
	args = append(args, id)
	return tx.Where(fmt.Sprintf("%s > %s OR (%s = %s AND %s > ?)", key, placeholder, key, placeholder, entryColumn(alias, "id")), args...)
}

func whereEntryOrder(tx *gorm.DB, alias string, bound model.Entry, idValue EntryIDValueFunc, after, inclusive bool) *gorm.DB {
	if tx.Dialector.Name() == "sqlite" {
		// Give the planner an indexed range before the precise timestamp/ID tie-break.
		op := " <= ?"
		if after {
			op = " >= ?"
		}
		tx = tx.Where(entryColumn(alias, "created_at_unix_ms")+op, bound.CreatedAt.UnixMilli())
	}
	key, placeholder, values := entryTimeBound(tx, alias, bound)
	seq, id := entryColumn(alias, "seq"), entryColumn(alias, "id")
	op, idOp := "<", "<"
	if after {
		op, idOp = ">", ">"
	}
	if inclusive {
		idOp += "="
	}
	args := append(append([]any(nil), values...), values...)
	var tie string
	if bound.Seq == nil {
		tie = fmt.Sprintf("%s IS NULL AND %s %s ?", seq, id, idOp)
		if after {
			tie = fmt.Sprintf("%s IS NOT NULL OR %s %s ?", seq, id, idOp)
		}
	} else {
		tie = fmt.Sprintf("%s IS NULL OR %s < ? OR (%s = ? AND %s %s ?)", seq, seq, seq, id, idOp)
		if after {
			tie = fmt.Sprintf("%s > ? OR (%s = ? AND %s %s ?)", seq, seq, id, idOp)
		}
		args = append(args, *bound.Seq, *bound.Seq)
	}
	args = append(args, idValue(bound.ID))
	return tx.Where(fmt.Sprintf("%s %s %s OR (%s = %s AND (%s))", key, op, placeholder, key, placeholder, tie), args...)
}
