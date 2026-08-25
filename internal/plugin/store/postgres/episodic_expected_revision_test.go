//go:build !nopostgresql

package postgres

import (
	"context"
	"testing"

	"github.com/chirino/memory-service/internal/config"
	registryepisodic "github.com/chirino/memory-service/internal/registry/episodic"
	registrymigrate "github.com/chirino/memory-service/internal/registry/migrate"
	"github.com/chirino/memory-service/internal/testutil/testpg"
	"github.com/stretchr/testify/require"
)

func TestPostgresExpectedRevisionBindsExactPredecessor(t *testing.T) {
	dbURL := testpg.StartPostgres(t)
	cfg := config.DefaultConfig()
	cfg.DBURL = dbURL
	cfg.DatastoreType = "postgres"
	cfg.EncryptionDBDisabled = true
	ctx := config.WithContext(context.Background(), &cfg)
	require.NoError(t, registrymigrate.RunAll(ctx))
	loader, err := registryepisodic.Select("postgres")
	require.NoError(t, err)
	loaded, err := loader(ctx)
	require.NoError(t, err)
	store := loaded.(*postgresEpisodicStore)
	ns := []string{"user", "alice", "expected-revision-race"}

	var revision int64
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		result, err := store.PutMemory(txCtx, registryepisodic.PutMemoryRequest{
			Namespace: ns, Key: "same-key", Value: map[string]interface{}{"writer": "initial"}, MemoryKind: "default/v1",
		})
		if err == nil {
			revision = result.Revision
		}
		return err
	}))

	checked := make(chan struct{})
	resume := make(chan struct{})
	store.putAfterRevisionCheck = func() {
		close(checked)
		<-resume
	}
	t.Cleanup(func() { store.putAfterRevisionCheck = nil })
	staleDone := make(chan error, 1)
	go func() {
		staleDone <- store.InWriteTx(ctx, func(txCtx context.Context) error {
			_, err := store.PutMemory(txCtx, registryepisodic.PutMemoryRequest{
				Namespace: ns, Key: "same-key", Value: map[string]interface{}{"writer": "stale"},
				MemoryKind: "default/v1", ExpectedRevision: &revision,
			})
			return err
		})
	}()
	<-checked

	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		_, err := store.PutMemory(txCtx, registryepisodic.PutMemoryRequest{
			Namespace: ns, Key: "same-key", Value: map[string]interface{}{"writer": "newer"}, MemoryKind: "default/v1",
		})
		return err
	}))
	close(resume)
	require.ErrorIs(t, <-staleDone, registryepisodic.ErrMemoryRevisionConflict)

	require.NoError(t, store.InReadTx(ctx, func(txCtx context.Context) error {
		active, err := store.GetMemory(txCtx, ns, "same-key", registryepisodic.ArchiveFilterExclude)
		require.NoError(t, err)
		require.Equal(t, "newer", active.Value["writer"])
		return nil
	}))
}
