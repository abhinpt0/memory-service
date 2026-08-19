//go:build !nomongo

package mongo

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// --- Defect 2: Mongo BSON conversion for attribute_types and dates ---

func TestSchemaVersionFromDocHandlesBsonM(t *testing.T) {
	t.Parallel()
	doc := bson.M{
		"_id":      "events/v1",
		"writable": true,
		"attribute_types": bson.M{
			"score": "number",
			"label": "string",
		},
		"created_at": time.Now().UTC(),
	}
	v := schemaVersionFromDoc(doc)
	if v.Name != "events/v1" {
		t.Errorf("Name = %q, want events/v1", v.Name)
	}
	if v.AttributeTypes["score"] != "number" {
		t.Errorf("AttributeTypes[score] = %q, want number", v.AttributeTypes["score"])
	}
	if v.AttributeTypes["label"] != "string" {
		t.Errorf("AttributeTypes[label] = %q, want string", v.AttributeTypes["label"])
	}
}

func TestSchemaVersionFromDocHandlesBsonD(t *testing.T) {
	t.Parallel()
	// bson.D is an ordered slice of key-value pairs — some driver versions return this.
	doc := bson.M{
		"_id":      "events/v2",
		"writable": false,
		"attribute_types": bson.D{
			{Key: "score", Value: "number"},
			{Key: "label", Value: "string"},
		},
		"created_at": bson.DateTime(time.Now().UnixMilli()),
	}
	v := schemaVersionFromDoc(doc)
	if v.Name != "events/v2" {
		t.Errorf("Name = %q, want events/v2", v.Name)
	}
	if v.AttributeTypes["score"] != "number" {
		t.Errorf("AttributeTypes[score] = %q, want number (bson.D)", v.AttributeTypes["score"])
	}
	if v.AttributeTypes["label"] != "string" {
		t.Errorf("AttributeTypes[label] = %q, want string (bson.D)", v.AttributeTypes["label"])
	}
	if v.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero when stored as bson.DateTime")
	}
}

func TestSchemaVersionFromDocHandlesBsonDateTimeInMigration(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Millisecond)
	doc := bson.M{
		"_id":        "mig-uuid",
		"source":     "profile/v1",
		"target":     "events/v1",
		"state":      "running",
		"created_at": bson.DateTime(now.UnixMilli()),
		"started_at": bson.DateTime(now.UnixMilli()),
	}
	m := migrationFromDoc(doc)
	if m.Source != "profile/v1" {
		t.Errorf("Source = %q, want profile/v1", m.Source)
	}
	// CreatedAt should be decoded from bson.DateTime
	if m.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero from bson.DateTime")
	}
	if m.StartedAt == nil {
		t.Fatal("StartedAt should not be nil")
	}
	if m.StartedAt.IsZero() {
		t.Error("StartedAt should not be zero from bson.DateTime")
	}
}

func TestSchemaVersionFromDocHandlesTimedotTimeInMigration(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	doc := bson.M{
		"_id":          "mig-uuid-2",
		"source":       "profile/v1",
		"target":       "events/v1",
		"state":        "succeeded",
		"created_at":   now,
		"started_at":   now,
		"completed_at": now,
	}
	m := migrationFromDoc(doc)
	if m.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero from time.Time")
	}
	if m.StartedAt == nil || m.StartedAt.IsZero() {
		t.Error("StartedAt should not be nil/zero from time.Time")
	}
	if m.CompletedAt == nil || m.CompletedAt.IsZero() {
		t.Error("CompletedAt should not be nil/zero from time.Time")
	}
}

func TestSchemaVersionFromDocHandlesDefaultV1(t *testing.T) {
	t.Parallel()
	// Simulate how the default/v1 document is stored (no attribute_types).
	doc := bson.M{
		"_id":        "default/v1",
		"writable":   true,
		"created_at": time.Now().UTC(),
	}
	v := schemaVersionFromDoc(doc)
	if v.Name != "default/v1" {
		t.Errorf("Name = %q, want default/v1", v.Name)
	}
	// Empty attribute_types are fine for the built-in default.
	_ = v.AttributeTypes
}

// TestSchemaVersionFromDocHandlesDefaultV1WithBsonDAttributes verifies that a
// realistically-shaped default/v1 document with bson.D attribute_types and
// bson.DateTime timestamps is decoded correctly (Item 10).
func TestSchemaVersionFromDocHandlesDefaultV1WithBsonDAttributes(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Millisecond)
	doc := bson.M{
		"_id":      "default/v1",
		"writable": true,
		// bson.D is an ordered slice — some driver versions return this from a real server.
		"attribute_types": bson.D{
			{Key: "namespace", Value: "string"},
			{Key: "sub", Value: "string"},
		},
		"created_at": bson.DateTime(now.UnixMilli()),
	}
	v := schemaVersionFromDoc(doc)
	if v.Name != "default/v1" {
		t.Errorf("Name = %q, want default/v1", v.Name)
	}
	if v.AttributeTypes["namespace"] != "string" {
		t.Errorf("AttributeTypes[namespace] = %q, want string", v.AttributeTypes["namespace"])
	}
	if v.AttributeTypes["sub"] != "string" {
		t.Errorf("AttributeTypes[sub] = %q, want string", v.AttributeTypes["sub"])
	}
	if v.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero when stored as bson.DateTime")
	}
}
