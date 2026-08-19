package episodic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"github.com/chirino/memory-service/internal/model"
	registryepisodic "github.com/chirino/memory-service/internal/registry/episodic"
	"github.com/ghodss/yaml"
)

// KindImportManifest is a file-backed bootstrap definition for an immutable
// database-backed MemoryKindVersion. The database remains authoritative after
// import; changing content requires a new canonical name.
type KindImportManifest struct {
	Kind               string            `json:"kind"`
	Name               string            `json:"name"`
	Attributes         map[string]string `json:"attributes"`
	ProjectionRego     string            `json:"projectionRego,omitempty"`
	ProjectionRegoFile string            `json:"projectionRegoFile,omitempty"`
	Writable           *bool             `json:"writable,omitempty"`
}

type policyDocumentHeader struct {
	Kind string `json:"kind"`
}

// ImportKindVersions recursively examines every *.yaml and *.yml document in a
// policy import directory and imports only documents whose kind is
// "memory-kind". Existing identical versions are idempotent. Conflicting
// content is logged and never overwrites the immutable database record. A
// directory with no memory-kind documents is valid because it may contain
// other policy types.
func ImportKindVersions(ctx context.Context, store registryepisodic.EpisodicStore, dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" || store == nil {
		return nil
	}
	var documents []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		extension := strings.ToLower(filepath.Ext(entry.Name()))
		if extension != ".yaml" && extension != ".yml" {
			return nil
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		documents = append(documents, relative)
		return nil
	})
	if err != nil {
		return fmt.Errorf("read policy import directory for memory kinds: %w", err)
	}
	sort.Strings(documents)
	for _, name := range documents {
		if err := importKindVersion(ctx, store, dir, name); err != nil {
			return err
		}
	}
	return nil
}

func importKindVersion(ctx context.Context, store registryepisodic.EpisodicStore, dir, filename string) error {
	manifestPath := filepath.Join(dir, filename)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read memory-kind manifest %s: %w", filename, err)
	}
	manifest, isMemoryKind, err := decodeKindImportManifest(raw, filename)
	if err != nil {
		return err
	}
	if !isMemoryKind {
		return nil
	}
	if _, _, err := ParseCanonicalKindName(manifest.Name); err != nil {
		return fmt.Errorf("memory-kind manifest %s: %w", filename, err)
	}
	if err := ValidateKindAttributeTypes(manifest.Attributes); err != nil {
		return fmt.Errorf("memory-kind manifest %s: %w", filename, err)
	}
	if manifest.ProjectionRego != "" && manifest.ProjectionRegoFile != "" {
		return fmt.Errorf("memory-kind manifest %s must set only one of projectionRego or projectionRegoFile", filename)
	}
	regoSource := manifest.ProjectionRego
	if manifest.ProjectionRegoFile != "" {
		regoPath, err := safeImportPath(filepath.Dir(manifestPath), manifest.ProjectionRegoFile)
		if err != nil {
			return fmt.Errorf("memory-kind manifest %s: %w", filename, err)
		}
		regoBytes, err := os.ReadFile(regoPath)
		if err != nil {
			return fmt.Errorf("read projection for memory-kind manifest %s: %w", filename, err)
		}
		regoSource = string(regoBytes)
	}
	if regoSource != "" {
		if _, err := CompileKindProjection(ctx, regoSource); err != nil {
			return fmt.Errorf("memory-kind manifest %s: %w", filename, err)
		}
	}
	writable := true
	if manifest.Writable != nil {
		writable = *manifest.Writable
	}
	version := model.MemoryKindVersion{
		Name: manifest.Name, AttributeTypes: manifest.Attributes,
		Writable: writable, CreatedAt: time.Now().UTC(),
	}
	if regoSource != "" {
		version.AttributesRego = &regoSource
	}

	var existing *model.MemoryKindVersion
	if err := store.InReadTx(ctx, func(txCtx context.Context) error {
		var loadErr error
		existing, loadErr = store.GetMemoryKindVersion(txCtx, manifest.Name)
		return loadErr
	}); err != nil {
		return fmt.Errorf("load imported memory kind %s: %w", manifest.Name, err)
	}
	if existing != nil {
		if !kindVersionsEqual(*existing, version) {
			log.Error("Memory-kind import conflict; stored immutable version was not changed", "name", manifest.Name, "manifest", filename)
			return nil // do not apply manifest defaults when its immutable content conflicts
		}
		log.Info("Memory-kind import already present", "name", manifest.Name, "manifest", filename)
	} else if err := store.InWriteTx(ctx, func(txCtx context.Context) error {
		_, createErr := store.CreateMemoryKindVersion(txCtx, version)
		return createErr
	}); err != nil {
		if errors.Is(err, registryepisodic.ErrMemoryKindVersionConflict) {
			log.Error("Memory-kind import conflict; stored immutable version was not changed", "name", manifest.Name, "manifest", filename)
			return nil
		}
		return fmt.Errorf("import memory kind %s: %w", manifest.Name, err)
	} else {
		log.Info("Imported immutable memory kind", "name", manifest.Name, "manifest", filename)
	}

	return nil
}

func decodeKindImportManifest(raw []byte, filename string) (KindImportManifest, bool, error) {
	// Convert through JSON so the manifest schema has one canonical set of
	// field tags while still accepting YAML policy documents.
	jsonDocument, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return KindImportManifest{}, false, fmt.Errorf("decode policy YAML document %s: %w", filename, err)
	}
	var header policyDocumentHeader
	if err := json.Unmarshal(jsonDocument, &header); err != nil {
		return KindImportManifest{}, false, fmt.Errorf("decode policy YAML document %s: %w", filename, err)
	}
	if header.Kind != "memory-kind" {
		return KindImportManifest{}, false, nil
	}

	var manifest KindImportManifest
	decoder := json.NewDecoder(bytes.NewReader(jsonDocument))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return KindImportManifest{}, false, fmt.Errorf("decode memory-kind manifest %s: %w", filename, err)
	}
	return manifest, true, nil
}

func safeImportPath(dir, relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", fmt.Errorf("projectionRegoFile must be relative")
	}
	clean := filepath.Clean(relative)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("projectionRegoFile escapes the import directory")
	}
	return filepath.Join(dir, clean), nil
}

func kindVersionsEqual(a, b model.MemoryKindVersion) bool {
	if a.Name != b.Name || a.Writable != b.Writable || len(a.AttributeTypes) != len(b.AttributeTypes) {
		return false
	}
	for key, value := range a.AttributeTypes {
		if b.AttributeTypes[key] != value {
			return false
		}
	}
	regoA, regoB := "", ""
	if a.AttributesRego != nil {
		regoA = *a.AttributesRego
	}
	if b.AttributesRego != nil {
		regoB = *b.AttributesRego
	}
	return regoA == regoB
}
