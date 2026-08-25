//go:build !nopostgresql

package postgres

import (
	"testing"

	"github.com/chirino/memory-service/internal/model"
)

func TestSchemaVersionsEqualIncludesWritable(t *testing.T) {
	a := model.MemoryKindVersion{Name: "x/v1", Writable: false, AttributeTypes: map[string]string{}}
	b := a
	b.Writable = true
	if schemaVersionsEqual(a, b) {
		t.Fatal("schema definitions with different writable flags must conflict")
	}
}
