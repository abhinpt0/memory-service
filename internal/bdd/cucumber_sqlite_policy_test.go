//go:build auth_testfixtures

package bdd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/chirino/memory-service/internal/buildcaps"
	"github.com/chirino/memory-service/internal/config"
	"github.com/chirino/memory-service/internal/testutil/cucumber"
	"github.com/cucumber/godog"
)

// TestFeaturesSQLitePolicy runs the episodic policy and kind tests against an
// in-process SQLite server that is configured with a custom OPA policy directory.
//
// The custom authz.rego:
//   - Sub-namespace "authz-custom": requires input.kind == "authz/v1".
//   - All other user sub-namespaces: requires input.kind == "default/v1".
//   - Sub-namespace "denied-ns": always denied regardless of kind.
//   - Empty or wrong kind causes the default deny rule to fire.
//
// The custom filter.rego:
//   - Namespace prefix ending in "filter-malformed-test": returns kind := 42 (integer)
//     to exercise the malformed-output validation path → 500 / INTERNAL.
//   - All other non-admin callers: outputs kind: "default/v1" as a narrowing selector.
//   - Caller "default" family → intersects to "default/v1" (exact within family).
//   - Caller "other/v1" (exact, different family) → disjoint → empty result.
//   - Caller "" (all kinds) → policy narrows to "default/v1".
func TestFeaturesSQLitePolicy(t *testing.T) {
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
	cfg.APIKeys = map[string]string{
		"test-agent-key": "test-agent-key",
	}
	cfg.AdminUsers = bddAdminUsers()
	cfg.AuditorUsers = bddAuditorUsers()
	cfg.IndexerUsers = bddIndexerUsers()
	cfg.Listener.Port = 0
	cfg.Listener.EnableTLS = false

	// Policy files are embedded from testdata/features-policy/.
	policyDir := filepath.Join("testdata", "features-policy")

	featureFiles := []string{
		filepath.Join(policyDir, "memory-kind-policy-rest.feature"),
		filepath.Join(policyDir, "memory-kind-policy-grpc.feature"),
	}

	runBDDFeaturesWithScenarioSetupAndTags(
		t, "sqlite-policy", featureFiles, "", "", &cfg, nil, nil,
		newSQLitePolicyScenarioSetup(t, cfg, policyDir),
		bddScenarioConcurrency(), sqliteTagFilter(),
	)
}

// newSQLitePolicyScenarioSetup returns a ScenarioSetupFunc that spins up a
// fresh SQLite server per scenario with a custom episodic policy directory.
func newSQLitePolicyScenarioSetup(t *testing.T, baseCfg config.Config, policyDir string) cucumber.ScenarioSetupFunc {
	t.Helper()

	// Resolve the absolute path to the policy directory at setup time.
	absPolicyDir, err := filepath.Abs(policyDir)
	if err != nil {
		t.Fatalf("resolve policy dir: %v", err)
	}

	return func(ctx context.Context, s *cucumber.TestScenario, sc *godog.Scenario) (func(context.Context) error, error) {
		_ = ctx
		_ = sc

		tempDir, err := os.MkdirTemp("", "memory-service-sqlite-policy-"+s.ScenarioUID+"-")
		if err != nil {
			return nil, err
		}
		prom := newMockPrometheus()

		cfg := baseCfg
		cfg.DBURL = filepath.Join(tempDir, "memory.db")
		cfg.PrometheusURL = prom.Server.URL
		cfg.PolicyImportDir = absPolicyDir

		cleanup, err := startScenarioServer(s, &cfg, &SQLiteTestDB{DBURL: cfg.DBURL}, map[string]interface{}{
			"mockPrometheus": prom,
		})
		if err != nil {
			prom.Server.Close()
			_ = os.RemoveAll(tempDir)
			return nil, err
		}

		return func(context.Context) error {
			return errors.Join(cleanup(context.Background()), closePrometheusAndDir(prom, tempDir))
		}, nil
	}
}
