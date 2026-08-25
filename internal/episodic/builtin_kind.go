package episodic

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/chirino/memory-service/internal/model"
)

//go:embed default-v1/memory-kind.yaml
var builtinDefaultKindManifest []byte

//go:embed default-v1/projection.rego
var builtinDefaultKindProjectionRego string

// BuiltinDefaultKindVersion returns the immutable default/v1 definition used to
// seed every primary datastore. The manifest and Rego source are embedded files
// so all datastore implementations use one canonical definition.
func BuiltinDefaultKindVersion(ctx context.Context) (model.MemoryKindVersion, error) {
	manifest, isMemoryKind, err := decodeKindImportManifest(builtinDefaultKindManifest, "default-v1/memory-kind.yaml")
	if err != nil {
		return model.MemoryKindVersion{}, fmt.Errorf("decode embedded default memory-kind manifest: %w", err)
	}
	if !isMemoryKind || manifest.Name != DefaultKindName {
		return model.MemoryKindVersion{}, fmt.Errorf("embedded default memory-kind manifest must define kind %q named %q", "memory-kind", DefaultKindName)
	}
	if manifest.ProjectionRego != "" || manifest.ProjectionRegoFile != "projection.rego" {
		return model.MemoryKindVersion{}, fmt.Errorf("embedded default memory-kind manifest must reference projection.rego")
	}
	if err := ValidateKindAttributeTypes(manifest.Attributes); err != nil {
		return model.MemoryKindVersion{}, fmt.Errorf("embedded default memory-kind manifest: %w", err)
	}
	if _, err := CompileKindProjection(ctx, builtinDefaultKindProjectionRego); err != nil {
		return model.MemoryKindVersion{}, fmt.Errorf("embedded default memory-kind projection: %w", err)
	}
	writable := true
	if manifest.Writable != nil {
		writable = *manifest.Writable
	}
	regoSource := builtinDefaultKindProjectionRego
	return model.MemoryKindVersion{
		Name:           manifest.Name,
		AttributeTypes: manifest.Attributes,
		AttributesRego: &regoSource,
		Writable:       writable,
	}, nil
}
