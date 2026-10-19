//go:build !nopostgresql

package pgstore

import (
	"bytes"
	"context"
	"testing"

	"github.com/chirino/memory-service/internal/config"
	"github.com/chirino/memory-service/internal/testutil/testpg"
	"github.com/stretchr/testify/require"
)

func TestDeleteIsIdempotent(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.DBURL = testpg.StartPostgres(t)
	cfg.TempDir = t.TempDir()
	ctx := config.WithContext(context.Background(), &cfg)

	attachmentStore, err := load(ctx)
	require.NoError(t, err)
	stored, err := attachmentStore.Store(ctx, bytes.NewBufferString("attachment"), 1024, "text/plain")
	require.NoError(t, err)

	require.NoError(t, attachmentStore.Delete(ctx, stored.StorageKey))
	require.NoError(t, attachmentStore.Delete(ctx, stored.StorageKey))
}
