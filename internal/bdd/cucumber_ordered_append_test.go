//go:build auth_testfixtures

package bdd

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/chirino/memory-service/internal/buildcaps"
	"github.com/chirino/memory-service/internal/cmd/serve"
	"github.com/chirino/memory-service/internal/config"
	"github.com/chirino/memory-service/internal/testutil/cucumber"
	"github.com/chirino/memory-service/internal/testutil/testmongo"
	"github.com/chirino/memory-service/internal/testutil/testpg"
	"github.com/stretchr/testify/require"
)

// Cover the REST/gRPC append contract without unrelated cache/vector providers.
func TestFeaturesOrderedAppend(t *testing.T) {
	for _, backend := range []struct {
		name          string
		mongo, outbox bool
	}{
		{name: "postgres"}, {name: "mongo", mongo: true}, {name: "mongo-atomic", mongo: true, outbox: true},
	} {
		t.Run(backend.name, func(t *testing.T) {
			cfg := defaultBDDConfig()
			cfg.Mode = config.ModeTesting
			cfg.CacheType = "none"
			cfg.VectorType = "none"
			cfg.SearchSemanticEnabled = false
			cfg.AttachType = "fs"
			cfg.AttachFSDir = t.TempDir()
			cfg.OutboxEnabled = backend.outbox
			cfg.EncryptionDBDisabled = true
			cfg.EncryptionAttachmentsDisabled = true
			cfg.AdminUsers = bddAdminUsers()
			cfg.AuditorUsers = bddAuditorUsers()
			cfg.IndexerUsers = bddIndexerUsers()
			cfg.Listener.Port = 0
			cfg.Listener.EnableTLS = false
			var db cucumber.TestDB
			if backend.mongo {
				if !buildcaps.MongoDB {
					requireCapabilities(t, "mongo")
				}
				cfg.DatastoreType = "mongo"
				cfg.DBURL = testmongo.StartMongo(t)
				db = &MongoTestDB{DBURL: cfg.DBURL}
			} else {
				if !buildcaps.PostgreSQL {
					requireCapabilities(t, "postgresql")
				}
				cfg.DatastoreType = "postgres"
				cfg.DBURL = testpg.StartPostgres(t)
				db = &PostgresTestDB{DBURL: cfg.DBURL}
			}
			ctx := config.WithContext(context.Background(), &cfg)
			srv, err := serve.StartServer(ctx, &cfg)
			require.NoError(t, err)
			t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
			features := []string{
				filepath.Join("testdata", "features", "entries-seq-rest.feature"),
				filepath.Join("testdata", "features-grpc", "entries-seq-retry-grpc.feature"),
			}
			if backend.mongo && !backend.outbox {
				features = append(features,
					filepath.Join("testdata", "features-mongo", "entries-seq-partial-rest.feature"),
					filepath.Join("testdata", "features-mongo", "entries-seq-partial-grpc.feature"),
				)
			}
			runBDDFeaturesWithConcurrency(t, backend.name, features, fmt.Sprintf("http://localhost:%d", srv.Running.Port), fmt.Sprintf("localhost:%d", srv.Running.Port), &cfg, db, map[string]interface{}{"grpcAddr": fmt.Sprintf("localhost:%d", srv.Running.Port)}, 1)
		})
	}
}
