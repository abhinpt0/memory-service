//go:build !nomongo

package mongo

import (
	"context"
	"testing"

	"github.com/chirino/memory-service/internal/config"
	registryepisodic "github.com/chirino/memory-service/internal/registry/episodic"
	"github.com/chirino/memory-service/internal/testutil/testmongo"
	"github.com/stretchr/testify/require"
)

func TestValidateEpisodicTransactionTopology(t *testing.T) {
	require.NoError(t, validateEpisodicTransactionTopology("rs0", ""))
	require.NoError(t, validateEpisodicTransactionTopology("", "isdbgrid"))
	err := validateEpisodicTransactionTopology("", "")
	require.ErrorContains(t, err, "replica set or mongos")
	require.ErrorContains(t, err, "standalone MongoDB is unsupported")
}

func TestEpisodicMongoLoaderRejectsStandaloneTopology(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.DatastoreType = "mongo"
	cfg.DBURL = testmongo.StartStandaloneMongo(t)
	ctx := config.WithContext(context.Background(), &cfg)
	loader, err := registryepisodic.Select("mongo")
	require.NoError(t, err)
	_, err = loader(ctx)
	require.ErrorContains(t, err, "replica set or mongos")
	require.ErrorContains(t, err, "standalone MongoDB is unsupported")
}
