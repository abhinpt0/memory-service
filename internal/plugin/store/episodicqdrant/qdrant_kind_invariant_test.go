package episodicqdrant

import (
	"context"
	"testing"

	registryepisodic "github.com/chirino/memory-service/internal/registry/episodic"
	"github.com/google/uuid"
	pb "github.com/qdrant/go-client/qdrant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type waitPointsClient struct {
	pb.PointsClient
	upsert *pb.UpsertPoints
	delete *pb.DeletePoints
	status pb.UpdateStatus
}

func (f *waitPointsClient) Upsert(_ context.Context, req *pb.UpsertPoints, _ ...grpc.CallOption) (*pb.PointsOperationResponse, error) {
	f.upsert = req
	return &pb.PointsOperationResponse{Result: &pb.UpdateResult{Status: f.status}}, nil
}

func (f *waitPointsClient) Delete(_ context.Context, req *pb.DeletePoints, _ ...grpc.CallOption) (*pb.PointsOperationResponse, error) {
	f.delete = req
	return &pb.PointsOperationResponse{Result: &pb.UpdateResult{Status: f.status}}, nil
}

func TestQdrantMutationsWaitForCompletion(t *testing.T) {
	fake := &waitPointsClient{status: pb.UpdateStatus_Completed}
	client := &Client{points: fake, collectionName: "test"}
	memoryID := uuid.New()
	require.NoError(t, client.UpsertMemoryVectors(context.Background(), []registryepisodic.MemoryVectorUpsert{{
		MemoryID: memoryID, FieldName: "body", Embedding: []float32{1}, MemoryKind: "default/v1", MemoryRevision: 1,
	}}))
	require.NotNil(t, fake.upsert.Wait)
	require.True(t, *fake.upsert.Wait)
	require.NoError(t, client.DeleteMemoryVectors(context.Background(), memoryID))
	require.NotNil(t, fake.delete.Wait)
	require.True(t, *fake.delete.Wait)

	fake.status = pb.UpdateStatus_Acknowledged
	require.ErrorContains(t, client.DeleteMemoryVectors(context.Background(), memoryID), "did not complete")
}

// TestUpsertMemoryVectorsRejectsEmptyKind verifies that UpsertMemoryVectors returns
// an error immediately when any item has an empty MemoryKind field, before any
// network call is made. This enforces the fresh-only kind invariant.
func TestUpsertMemoryVectorsRejectsEmptyKind(t *testing.T) {
	t.Parallel()
	c := &Client{
		// points is intentionally nil — the guard fires before any network call.
		collectionName: "test",
	}

	memID := uuid.New()
	items := []registryepisodic.MemoryVectorUpsert{
		{
			MemoryID:   memID,
			FieldName:  "content",
			Embedding:  []float32{0.1, 0.2},
			MemoryKind: "", // empty — must be rejected
		},
	}
	err := c.UpsertMemoryVectors(context.Background(), items)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MemoryVectorUpsert.MemoryKind must be non-empty")
}

// TestUpsertMemoryVectorsAcceptsNonEmptyKind verifies that items with a non-empty
// MemoryKind pass the guard. (The subsequent network call would fail with a nil
// client, so we only test that the guard does not fire.)
func TestUpsertMemoryVectorsNonEmptyKindPassesGuard(t *testing.T) {
	t.Parallel()
	c := &Client{
		// points is nil — will panic on the actual Qdrant call, but the guard
		// must not return an error before that.
		collectionName: "test",
	}

	memID := uuid.New()
	items := []registryepisodic.MemoryVectorUpsert{
		{
			MemoryID:   memID,
			FieldName:  "content",
			Embedding:  []float32{0.1, 0.2},
			MemoryKind: "default/v1",
		},
	}
	// The guard should pass. The call will panic or nil-deref after the guard.
	// Recover the panic to verify behaviour without a live server.
	defer func() {
		r := recover()
		// A nil-pointer dereference from the points client is expected — that
		// means the guard passed and execution reached the network layer.
		assert.NotNil(t, r, "expected panic from nil points client, got none")
	}()
	_ = c.UpsertMemoryVectors(context.Background(), items) //nolint:errcheck
}

// TestSchemaFromPayload tests the payload decoder for the memory_kind field.
func TestSchemaFromPayload(t *testing.T) {
	t.Parallel()

	t.Run("nil payload returns empty", func(t *testing.T) {
		assert.Equal(t, "", schemaFromPayload(nil))
	})
	t.Run("missing key returns empty", func(t *testing.T) {
		assert.Equal(t, "", schemaFromPayload(map[string]*pb.Value{}))
	})
	t.Run("nil value returns empty", func(t *testing.T) {
		assert.Equal(t, "", schemaFromPayload(map[string]*pb.Value{
			"memory_kind": nil,
		}))
	})
	t.Run("string value returned", func(t *testing.T) {
		assert.Equal(t, "default/v1", schemaFromPayload(map[string]*pb.Value{
			"memory_kind": {Kind: &pb.Value_StringValue{StringValue: "default/v1"}},
		}))
	})
	t.Run("non-string value (integer) returns empty string via GetStringValue", func(t *testing.T) {
		// An integer payload entry has no StringValue — GetStringValue returns "".
		result := schemaFromPayload(map[string]*pb.Value{
			"memory_kind": {Kind: &pb.Value_IntegerValue{IntegerValue: 42}},
		})
		// GetStringValue on a non-string Value returns the zero string, which
		// means SearchMemoryVectors would skip this candidate (schema == "").
		assert.Equal(t, "", result)
	})
}

// TestSearchMemoryVectorsSkipsEmptySchema verifies that the search loop skips
// candidates whose payload yields an empty memory_kind via schemaFromPayload.
// This is a white-box test of the skip logic extracted into schemaFromPayload.
func TestSearchMemoryVectorsSkipsEmptySchema(t *testing.T) {
	t.Parallel()
	// A payload with a non-string memory_kind (integer 42) produces an empty
	// schema via GetStringValue, so the candidate is skipped.
	payload := map[string]*pb.Value{
		"memory_kind": {Kind: &pb.Value_IntegerValue{IntegerValue: 42}},
	}
	schema := schemaFromPayload(payload)
	assert.Equal(t, "", schema,
		"non-string memory_kind in Qdrant payload must produce empty schema → candidate skipped")
}
