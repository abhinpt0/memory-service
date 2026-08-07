package grpc

import (
	"testing"

	pb "github.com/chirino/memory-service/internal/generated/pb/memory/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestValidateGRPCConversationPatchEmptyMetadataIsNoOp(t *testing.T) {
	patch, err := validateGRPCConversationPatch(&pb.UpdateConversationRequest{
		Metadata: &structpb.Struct{},
	})
	require.NoError(t, err)
	assert.Nil(t, patch)
}

func TestValidateGRPCConversationPatchMetadataMutation(t *testing.T) {
	metadata, err := structpb.NewStruct(map[string]interface{}{
		"a.b":    "value",
		"delete": nil,
	})
	require.NoError(t, err)

	patch, err := validateGRPCConversationPatch(&pb.UpdateConversationRequest{Metadata: metadata})
	require.NoError(t, err)
	require.NotNil(t, patch)
	assert.True(t, patch.metadataPresent)
	assert.Equal(t, "value", patch.metadataPatch["a.b"])
	assert.Contains(t, patch.metadataPatch, "delete")
	assert.Nil(t, patch.metadataPatch["delete"])
}
