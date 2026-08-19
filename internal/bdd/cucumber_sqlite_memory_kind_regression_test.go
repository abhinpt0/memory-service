package bdd

import (
	"path/filepath"
	"testing"

	"github.com/chirino/memory-service/internal/buildcaps"
	"github.com/chirino/memory-service/internal/config"
)

// TestFeaturesSQLiteMemoryKindRegressions is a deliberately small BDD runner
// for kind/archive contract regressions that should not require the broad suite.
func TestFeaturesSQLiteMemoryKindRegressions(t *testing.T) {
	if !buildcaps.SQLite {
		requireCapabilities(t, "sqlite")
	}

	cfg := defaultBDDConfig()
	cfg.Mode = config.ModeTesting
	cfg.DatastoreType = "sqlite"
	cfg.CacheType = "none"
	cfg.AttachType = "fs"
	cfg.VectorType = "none"
	cfg.SearchSemanticEnabled = false
	cfg.EncryptionDBDisabled = true
	cfg.EncryptionAttachmentsDisabled = true
	cfg.APIKeys = map[string]string{"test-agent-key": "test-agent-key"}
	cfg.AdminUsers = bddAdminUsers()
	cfg.AuditorUsers = bddAuditorUsers()
	cfg.IndexerUsers = bddIndexerUsers()
	cfg.Listener.Port = 0
	cfg.Listener.EnableTLS = false

	featureFiles := []string{
		filepath.Join("testdata", "features", "memory-kind-rest.feature"),
		filepath.Join("testdata", "features-grpc", "memories-grpc.feature"),
	}
	runBDDFeaturesWithScenarioSetupAndTags(
		t, "sqlite-memory-kind-regression", featureFiles, "", "", &cfg, nil, nil,
		newSQLiteScenarioSetup(t, cfg), bddScenarioConcurrency(), "@memory-kind-regression && "+sqliteTagFilter(),
	)
}
