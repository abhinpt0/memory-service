package grpc

import (
	pb "github.com/chirino/memory-service/internal/generated/pb/memory/v1"
	registrystore "github.com/chirino/memory-service/internal/registry/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// validatedGRPCConversationPatch holds the validated and parsed fields from a gRPC UpdateConversationRequest.
type validatedGRPCConversationPatch struct {
	title           *string                     // nil means not present
	archived        *bool                       // nil means not present
	metadataPatch   registrystore.MetadataPatch // nil means not present
	metadataPresent bool                        // true when a non-null metadata patch was supplied
}

// validateGRPCConversationPatch validates a gRPC UpdateConversationRequest before any datastore
// mutations. Returns nil,nil if the patch is nil or empty.
func validateGRPCConversationPatch(patch *pb.UpdateConversationRequest) (*validatedGRPCConversationPatch, error) {
	if patch == nil {
		return nil, nil
	}

	result := &validatedGRPCConversationPatch{}
	hasMutation := false

	// Validate title if present.
	if patch.Title != nil {
		if len(*patch.Title) > 500 {
			return nil, status.Error(codes.InvalidArgument, "conversation_patch.title exceeds maximum length of 500 characters")
		}
		result.title = patch.Title
		hasMutation = true
	}

	// Validate archived if present.
	if patch.Archived != nil {
		result.archived = patch.Archived
		hasMutation = true
	}

	// Validate metadata if present.
	if patch.GetMetadata() != nil {
		patchBytes, marshalErr := patch.GetMetadata().MarshalJSON()
		if marshalErr != nil {
			return nil, status.Error(codes.InvalidArgument, "conversation_patch.metadata encoding failed")
		}
		var err error
		result.metadataPatch, err = registrystore.DecodeMetadataPatch(patchBytes)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "conversation_patch.metadata: %v", err)
		}
		if len(result.metadataPatch) > 0 {
			result.metadataPresent = true
			hasMutation = true
		}
	}

	if !hasMutation {
		return nil, nil
	}

	return result, nil
}

// needsUnarchiveBeforeWrite returns true if the validated patch requests unarchiving (archived=false).
func (v *validatedGRPCConversationPatch) needsUnarchiveBeforeWrite() bool {
	return v != nil && v.archived != nil && !*v.archived
}

func (v *validatedGRPCConversationPatch) withoutArchived() *validatedGRPCConversationPatch {
	if v == nil {
		return nil
	}
	result := *v
	result.archived = nil
	if result.title == nil && !result.metadataPresent {
		return nil
	}
	return &result
}

func (v *validatedGRPCConversationPatch) changesAgainst(conversation *registrystore.ConversationDetail) *validatedGRPCConversationPatch {
	if v == nil || conversation == nil {
		return v
	}
	changes := &validatedGRPCConversationPatch{}
	if v.title != nil && conversation.Title != *v.title {
		changes.title = v.title
	}
	if v.archived != nil && (conversation.ArchivedAt != nil) != *v.archived {
		changes.archived = v.archived
	}
	changes.metadataPatch = v.metadataPatch.ChangesAgainst(conversation.Metadata)
	changes.metadataPresent = len(changes.metadataPatch) > 0
	if changes.title == nil && changes.archived == nil && !changes.metadataPresent {
		return nil
	}
	return changes
}

// validateGRPCConversationPatchForOwnerOnly checks if the patch requires owner-level
// authorization (i.e., it includes the archived field).
func validateGRPCConversationPatchForOwnerOnly(patch *validatedGRPCConversationPatch, hasOwnerAccess bool) error {
	if patch != nil && patch.archived != nil && !hasOwnerAccess {
		return status.Error(codes.PermissionDenied, "only conversation owners can archive or unarchive conversations")
	}
	return nil
}
