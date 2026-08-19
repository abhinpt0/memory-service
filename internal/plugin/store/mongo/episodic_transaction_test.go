//go:build !nomongo

package mongo_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/chirino/memory-service/internal/model"
	registryepisodic "github.com/chirino/memory-service/internal/registry/episodic"
	"github.com/stretchr/testify/require"
)

func TestMongoWriteTransactionRollsBackAllEpisodicWrites(t *testing.T) {
	store, ctx := setupMongoEpisodicStore(t)
	ns := []string{"user", "alice", "transaction-rollback"}
	wantErr := errors.New("injected failure between paired writes")

	err := store.InWriteTx(ctx, func(txCtx context.Context) error {
		if _, err := store.PutMemory(txCtx, registryepisodic.PutMemoryRequest{
			Namespace: ns, Key: "first", Value: map[string]interface{}{"n": 1}, MemoryKind: "default/v1",
		}); err != nil {
			return err
		}
		if _, err := store.PutMemory(txCtx, registryepisodic.PutMemoryRequest{
			Namespace: ns, Key: "second", Value: map[string]interface{}{"n": 2}, MemoryKind: "default/v1",
		}); err != nil {
			return err
		}
		return wantErr
	})
	require.ErrorIs(t, err, wantErr)

	require.NoError(t, store.InReadTx(ctx, func(txCtx context.Context) error {
		for _, key := range []string{"first", "second"} {
			item, err := store.GetMemory(txCtx, ns, key, registryepisodic.ArchiveFilterInclude)
			require.NoError(t, err)
			require.Nil(t, item, "write %q escaped the aborted MongoDB transaction", key)
		}
		return nil
	}))
}

func TestMongoTransactionKeepsKindAndContentOnOneSnapshot(t *testing.T) {
	store, ctx := setupMongoEpisodicStore(t)
	ns := []string{"user", "alice", "kind-snapshot"}
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		for _, name := range []string{"kind-a/v1", "kind-b/v1"} {
			if _, err := store.CreateMemoryKindVersion(txCtx, model.MemoryKindVersion{
				Name: name, AttributeTypes: map[string]string{}, Writable: true, CreatedAt: time.Now().UTC(),
			}); err != nil {
				return err
			}
		}
		_, err := store.PutMemory(txCtx, registryepisodic.PutMemoryRequest{
			Namespace: ns, Key: "same-key", Value: map[string]interface{}{"version": "a"}, MemoryKind: "kind-a/v1",
		})
		return err
	}))

	lookupDone := make(chan struct{})
	replacementDone := make(chan error, 1)
	var signalOnce sync.Once
	readDone := make(chan error, 1)
	go func() {
		readDone <- store.InWriteTx(ctx, func(txCtx context.Context) error {
			kind, found, err := store.GetMemoryRowKind(txCtx, ns, "same-key", registryepisodic.ArchiveFilterExclude)
			if err != nil {
				return err
			}
			require.True(t, found)
			require.Equal(t, "kind-a/v1", kind)
			signalOnce.Do(func() { close(lookupDone) })
			if err := <-replacementDone; err != nil {
				return err
			}
			item, err := store.GetMemory(txCtx, ns, "same-key", registryepisodic.ArchiveFilterExclude)
			if err != nil {
				return err
			}
			require.NotNil(t, item)
			require.Equal(t, "kind-a/v1", item.MemoryKind)
			require.Equal(t, "a", item.Value["version"])
			return nil
		})
	}()
	<-lookupDone

	go func() {
		replacementDone <- store.InWriteTx(ctx, func(txCtx context.Context) error {
			_, err := store.PutMemory(txCtx, registryepisodic.PutMemoryRequest{
				Namespace: ns, Key: "same-key", Value: map[string]interface{}{"version": "b"}, MemoryKind: "kind-b/v1",
			})
			return err
		})
	}()
	require.NoError(t, <-readDone)
}
