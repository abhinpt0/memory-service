//go:build !nopostgresql

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chirino/memory-service/internal/model"
	registryepisodic "github.com/chirino/memory-service/internal/registry/episodic"
	"github.com/stretchr/testify/require"
)

func TestPostgresPutRejectsChangedAbsentPredecessor(t *testing.T) {
	store, ctx := setupPostgresEpisodicStore(t)
	ns := []string{"user", "alice", "optimistic-predecessor"}
	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		_, err := store.CreateMemoryKindVersion(txCtx, model.MemoryKindVersion{
			Name: "protected/v1", AttributeTypes: map[string]string{}, Writable: true, CreatedAt: time.Now().UTC(),
		})
		return err
	}))

	observedAbsent := make(chan struct{})
	resume := make(chan struct{})
	userDone := make(chan error, 1)
	go func() {
		userDone <- store.InWriteTx(ctx, func(txCtx context.Context) error {
			predecessor, err := store.GetMemoryPredecessor(txCtx, ns, "same-key")
			if err != nil {
				return err
			}
			require.Nil(t, predecessor)
			close(observedAbsent)
			<-resume
			_, err = store.PutMemory(txCtx, registryepisodic.PutMemoryRequest{
				Namespace: ns, Key: "same-key", Value: map[string]interface{}{"owner": "user"}, MemoryKind: "default/v1",
				AuthorizedPredecessor: &registryepisodic.MemoryPredecessorExpectation{Exists: false},
			})
			return err
		})
	}()
	<-observedAbsent

	require.NoError(t, store.InWriteTx(ctx, func(txCtx context.Context) error {
		_, err := store.PutMemory(txCtx, registryepisodic.PutMemoryRequest{
			Namespace: ns, Key: "same-key", Value: map[string]interface{}{"owner": "admin"}, MemoryKind: "protected/v1",
		})
		return err
	}))
	close(resume)
	require.ErrorIs(t, <-userDone, registryepisodic.ErrMemoryRevisionConflict)

	require.NoError(t, store.InReadTx(ctx, func(txCtx context.Context) error {
		active, err := store.GetMemory(txCtx, ns, "same-key", registryepisodic.ArchiveFilterExclude)
		require.NoError(t, err)
		require.Equal(t, "protected/v1", active.MemoryKind)
		require.Equal(t, "admin", active.Value["owner"])
		return nil
	}))
}

func TestPostgresConcurrentAbsentPutsLeaveOneActiveRow(t *testing.T) {
	store, ctx := setupPostgresEpisodicStore(t)
	ns := []string{"user", "alice", "concurrent-absent"}
	observed := make(chan struct{}, 2)
	resume := make(chan struct{})
	results := make(chan error, 2)

	for i := range 2 {
		i := i
		go func() {
			results <- store.InWriteTx(ctx, func(txCtx context.Context) error {
				predecessor, err := store.GetMemoryPredecessor(txCtx, ns, "same-key")
				if err != nil {
					return err
				}
				if predecessor != nil {
					return errors.New("expected absent predecessor")
				}
				observed <- struct{}{}
				<-resume
				_, err = store.PutMemory(txCtx, registryepisodic.PutMemoryRequest{
					Namespace: ns, Key: "same-key", Value: map[string]interface{}{"writer": i}, MemoryKind: "default/v1",
					AuthorizedPredecessor: &registryepisodic.MemoryPredecessorExpectation{Exists: false},
				})
				return err
			})
		}()
	}
	<-observed
	<-observed
	close(resume)

	var succeeded, conflicted int
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, registryepisodic.ErrMemoryRevisionConflict):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent put result: %v", err)
		}
	}
	require.Equal(t, 1, succeeded)
	require.Equal(t, 1, conflicted)
	active, err := store.GetMemory(ctx, ns, "same-key", registryepisodic.ArchiveFilterExclude)
	require.NoError(t, err)
	require.NotNil(t, active)
}

// TestPostgresKindLookupLocksTheSelectedRow proves that authorization cannot
// observe kind A and then return kind B under READ COMMITTED. PutMemory must wait
// for the transaction holding the kind lookup lock to finish.
func TestPostgresKindLookupLocksTheSelectedRow(t *testing.T) {
	store, ctx := setupPostgresEpisodicStore(t)
	ns := []string{"user", "alice", "kind-lock"}

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

	locked := make(chan struct{})
	release := make(chan struct{})
	readerDone := make(chan error, 1)
	go func() {
		readerDone <- store.InWriteTx(ctx, func(txCtx context.Context) error {
			kind, found, err := store.GetMemoryRowKind(txCtx, ns, "same-key", registryepisodic.ArchiveFilterExclude)
			if err != nil {
				return err
			}
			require.True(t, found)
			require.Equal(t, "kind-a/v1", kind)
			close(locked)
			<-release

			item, err := store.GetMemory(txCtx, ns, "same-key", registryepisodic.ArchiveFilterExclude)
			if err != nil {
				return err
			}
			require.NotNil(t, item)
			require.Equal(t, "kind-a/v1", item.MemoryKind)
			require.Equal(t, "a", item.Value["version"])
			return store.ArchiveMemory(txCtx, ns, "same-key", nil)
		})
	}()
	<-locked

	writerDone := make(chan error, 1)
	go func() {
		writerDone <- store.InWriteTx(ctx, func(txCtx context.Context) error {
			_, err := store.PutMemory(txCtx, registryepisodic.PutMemoryRequest{
				Namespace: ns, Key: "same-key", Value: map[string]interface{}{"version": "b"}, MemoryKind: "kind-b/v1",
			})
			return err
		})
	}()

	select {
	case err := <-writerDone:
		t.Fatalf("different-kind replacement committed while the authorized row was locked: %v", err)
	case <-time.After(200 * time.Millisecond):
		// Expected: the row lock keeps kind lookup and content selection coherent.
	}
	close(release)
	require.NoError(t, <-readerDone)
	require.NoError(t, <-writerDone)

	require.NoError(t, store.InReadTx(ctx, func(txCtx context.Context) error {
		active, err := store.GetMemory(txCtx, ns, "same-key", registryepisodic.ArchiveFilterExclude)
		require.NoError(t, err)
		require.Equal(t, "kind-b/v1", active.MemoryKind)
		archived, err := store.GetMemory(txCtx, ns, "same-key", registryepisodic.ArchiveFilterOnly)
		require.NoError(t, err)
		require.Equal(t, "kind-a/v1", archived.MemoryKind)
		return nil
	}))
}
