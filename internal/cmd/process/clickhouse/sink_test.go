package clickhouse

import (
	"context"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/require"
)

type cleanupBatch struct {
	driver.Batch
	closed bool
}

func (b *cleanupBatch) Append(...any) error { return nil }
func (b *cleanupBatch) Close() error        { b.closed = true; return nil }

type cleanupConn struct {
	driver.Conn
	batch *cleanupBatch
}

func (c *cleanupConn) PrepareBatch(context.Context, string, ...driver.PrepareBatchOption) (driver.Batch, error) {
	return c.batch, nil
}

func TestProjectionBatchClosesOnEarlyReturn(t *testing.T) {
	batch := &cleanupBatch{}
	sink := &ClickHouseSink{conn: &cleanupConn{batch: batch}, database: "test"}
	err := sink.writeProjectionRows(context.Background(), Batch{Projections: []ProjectionRow{
		{TableName: "projection", Multi: true},
		{TableName: "projection", Multi: false},
	}})
	require.ErrorContains(t, err, "mixes single-row and multi-row writes")
	require.True(t, batch.closed, "an early return without Send or an Append error must release the batch")
}
