package bdd

import (
	"path/filepath"
	"testing"

	"github.com/chirino/memory-service/internal/buildcaps"
	"github.com/chirino/memory-service/internal/config"
	"github.com/chirino/memory-service/internal/testutil/testredis"
)

func TestFeaturesEventBusRedis(t *testing.T) {
	if !buildcaps.SQLite || !buildcaps.Redis {
		requireCapabilities(t, "sqlite", "redis")
	}

	cfg := defaultBDDConfig()
	cfg.Mode = config.ModeTesting
	cfg.DatastoreType = "sqlite"
	cfg.DBURL = ""
	cfg.CacheType = "local"
	cfg.AttachType = "fs"
	cfg.VectorType = "none"
	cfg.SearchSemanticEnabled = false
	cfg.EncryptionDBDisabled = true
	cfg.EncryptionAttachmentsDisabled = true
	cfg.APIKeys = map[string]string{"test-agent-key": "test-agent-key"}
	cfg.AdminUsers = bddAdminUsers()
	cfg.AuditorUsers = bddAuditorUsers()
	cfg.IndexerUsers = bddIndexerUsers()
	cfg.EventBusType = "redis"
	cfg.RedisURL = testredis.StartRedis(t)
	cfg.Listener.Port = 0
	cfg.Listener.EnableTLS = false

	featureFiles := []string{filepath.Join("testdata", "features-eventbus", "sse-delivery-rest.feature")}
	runBDDFeaturesWithScenarioSetupAndTags(t, "eventbus-redis", featureFiles, "", "", &cfg, nil, nil, newSQLiteScenarioSetup(t, cfg), 1, "")
}
