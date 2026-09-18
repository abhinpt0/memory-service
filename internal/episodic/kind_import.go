package episodic

import (
	"bytes"
	"context"
	"encoding/csv"
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

// ImportKindVersions examines the files and directories in policyImportPath and
// imports only documents whose kind is "memory-kind". Directory entries are
// searched recursively for *.yaml and *.yml files. Explicit file entries are
// examined regardless of their extension. Existing identical versions are
// idempotent. Conflicting content is logged and never overwrites the immutable
// database record.
func ImportKindVersions(ctx context.Context, store registryepisodic.EpisodicStore, policyImportPath string) error {
	if strings.TrimSpace(policyImportPath) == "" || store == nil {
		return nil
	}
	documents, err := kindImportDocuments(policyImportPath)
	if err != nil {
		return err
	}
	for _, filename := range documents {
		if err := importKindVersion(ctx, store, filename); err != nil {
			return err
		}
	}
	return nil
}

func kindImportDocuments(policyImportPath string) ([]string, error) {
	paths, err := parsePolicyImportPath(policyImportPath)
	if err != nil {
		return nil, err
	}
	documents := make([]string, 0, len(paths))
	seen := make(map[string]struct{})
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("read policy import path %s: %w", path, err)
		}
		if !info.IsDir() {
			if strings.EqualFold(filepath.Ext(path), ".rego") {
				continue
			}
			documents = appendUniquePath(documents, seen, path)
			continue
		}

		var directoryDocuments []string
		err = filepath.WalkDir(path, func(filename string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			extension := strings.ToLower(filepath.Ext(entry.Name()))
			if extension == ".yaml" || extension == ".yml" {
				directoryDocuments = append(directoryDocuments, filename)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("read policy import directory %s for memory kinds: %w", path, err)
		}
		sort.Strings(directoryDocuments)
		for _, filename := range directoryDocuments {
			documents = appendUniquePath(documents, seen, filename)
		}
	}
	return documents, nil
}

func appendUniquePath(paths []string, seen map[string]struct{}, path string) []string {
	key, err := filepath.Abs(path)
	if err != nil {
		key = filepath.Clean(path)
	}
	if _, ok := seen[key]; ok {
		return paths
	}
	seen[key] = struct{}{}
	return append(paths, path)
}

func parsePolicyImportPath(policyImportPath string) ([]string, error) {
	reader := csv.NewReader(strings.NewReader(policyImportPath))
	reader.TrimLeadingSpace = true
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse policy import path: %w", err)
	}
	var paths []string
	for _, record := range records {
		for _, path := range record {
			if path = strings.TrimSpace(path); path != "" {
				paths = append(paths, path)
			}
		}
	}
	return paths, nil
}

func importKindVersion(ctx context.Context, store registryepisodic.EpisodicStore, manifestPath string) error {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read memory-kind manifest %s: %w", manifestPath, err)
	}
	manifest, isMemoryKind, err := decodeKindImportManifest(raw, manifestPath)
	if err != nil {
		return err
	}
	if !isMemoryKind {
		return nil
	}
	if _, _, err := ParseCanonicalKindName(manifest.Name); err != nil {
		return fmt.Errorf("memory-kind manifest %s: %w", manifestPath, err)
	}
	if err := ValidateKindAttributeTypes(manifest.Attributes); err != nil {
		return fmt.Errorf("memory-kind manifest %s: %w", manifestPath, err)
	}
	if manifest.ProjectionRego != "" && manifest.ProjectionRegoFile != "" {
		return fmt.Errorf("memory-kind manifest %s must set only one of projectionRego or projectionRegoFile", manifestPath)
	}
	regoSource := manifest.ProjectionRego
	if manifest.ProjectionRegoFile != "" {
		regoPath, err := safeImportPath(filepath.Dir(manifestPath), manifest.ProjectionRegoFile)
		if err != nil {
			return fmt.Errorf("memory-kind manifest %s: %w", manifestPath, err)
		}
		regoBytes, err := os.ReadFile(regoPath)
		if err != nil {
			return fmt.Errorf("read projection for memory-kind manifest %s: %w", manifestPath, err)
		}
		regoSource = string(regoBytes)
	}
	if regoSource != "" {
		if _, err := CompileKindProjection(ctx, regoSource); err != nil {
			return fmt.Errorf("memory-kind manifest %s: %w", manifestPath, err)
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
			log.Error("Memory-kind import conflict; stored immutable version was not changed", "name", manifest.Name, "manifest", manifestPath)
			return nil // do not apply manifest defaults when its immutable content conflicts
		}
		log.Info("Memory-kind import already present", "name", manifest.Name, "manifest", manifestPath)
	} else if err := store.InWriteTx(ctx, func(txCtx context.Context) error {
		_, createErr := store.CreateMemoryKindVersion(txCtx, version)
		return createErr
	}); err != nil {
		if errors.Is(err, registryepisodic.ErrMemoryKindVersionConflict) {
			log.Error("Memory-kind import conflict; stored immutable version was not changed", "name", manifest.Name, "manifest", manifestPath)
			return nil
		}
		return fmt.Errorf("import memory kind %s: %w", manifest.Name, err)
	} else {
		log.Info("Imported immutable memory kind", "name", manifest.Name, "manifest", manifestPath)
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
