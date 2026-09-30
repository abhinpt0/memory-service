package bdd

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/chirino/memory-service/internal/buildcaps"
	"github.com/chirino/memory-service/internal/cmd/serve"
	"github.com/chirino/memory-service/internal/config"
	"github.com/chirino/memory-service/internal/testutil/testpg"
	"github.com/stretchr/testify/require"
)

func TestFeaturesEventBusPostgres(t *testing.T) {
	if !buildcaps.PostgreSQL {
		requireCapabilities(t, "postgresql")
	}

	dbURL := testpg.StartPostgres(t)
	cfg := defaultBDDConfig()
	cfg.Mode = config.ModeTesting
	cfg.DBURL = dbURL
	cfg.CacheType = "local"
	cfg.VectorType = "none"
	cfg.SearchSemanticEnabled = false
	cfg.EncryptionDBDisabled = true
	cfg.EncryptionAttachmentsDisabled = true
	cfg.APIKeys = map[string]string{"test-agent-key": "test-agent-key"}
	cfg.AdminUsers = bddAdminUsers()
	cfg.AuditorUsers = bddAuditorUsers()
	cfg.IndexerUsers = bddIndexerUsers()
	cfg.EventBusType = "postgres"
	cfg.Listener.Port = 0
	cfg.Listener.EnableTLS = false

	srv, err := serve.StartServer(config.WithContext(context.Background(), &cfg), &cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	apiURL := fmt.Sprintf("http://localhost:%d", srv.Running.Port)
	featureFiles := []string{filepath.Join("testdata", "features-eventbus", "sse-delivery-rest.feature")}
	runBDDFeaturesWithConcurrency(t, "eventbus-postgres", featureFiles, apiURL, "", &cfg, &PostgresTestDB{DBURL: dbURL}, nil, 1)
}
