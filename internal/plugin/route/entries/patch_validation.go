package entries

import (
	"bytes"
	"encoding/json"

	registrystore "github.com/chirino/memory-service/internal/registry/store"
)

// validatedConversationPatch holds the validated and parsed fields from a conversationPatch.
type validatedConversationPatch struct {
	title           *string                     // nil means not present, empty string means clear
	archived        *bool                       // nil means not present
	metadataPatch   registrystore.MetadataPatch // nil means not present
	metadataPresent bool                        // true when a non-null metadata patch was supplied
}

// validateConversationPatch validates a conversationPatch JSON payload before any datastore
// mutations. Returns nil,nil if the patch is empty, whitespace, absent, or JSON null.
// Returns nil,nil if the patch is an empty object {} or contains only metadata:null.
func validateConversationPatch(raw json.RawMessage) (*validatedConversationPatch, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}

	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &rawMap); err != nil || rawMap == nil {
		return nil, &registrystore.BadRequestError{Message: "invalid conversationPatch: must be a JSON object"}
	}

	result := &validatedConversationPatch{}
	hasMutation := false

	// Validate title if present.
	if titleRaw, ok := rawMap["title"]; ok {
		titleTrimmed := bytes.TrimSpace(titleRaw)
		if bytes.Equal(titleTrimmed, []byte("null")) {
			empty := ""
			result.title = &empty
		} else {
			var s string
			if err := json.Unmarshal(titleTrimmed, &s); err != nil {
				return nil, &registrystore.BadRequestError{Message: "invalid conversationPatch.title: must be a string or null"}
			}
			if len(s) > 500 {
				return nil, &registrystore.BadRequestError{Message: "conversationPatch.title exceeds maximum length of 500 characters"}
			}
			result.title = &s
		}
		hasMutation = true
	}

	// Validate archived if present.
	if archivedRaw, ok := rawMap["archived"]; ok {
		archivedTrimmed := bytes.TrimSpace(archivedRaw)
		if bytes.Equal(archivedTrimmed, []byte("null")) {
			return nil, &registrystore.BadRequestError{Message: "invalid conversationPatch.archived: must be a boolean"}
		}
		var b bool
		if err := json.Unmarshal(archivedTrimmed, &b); err != nil {
			return nil, &registrystore.BadRequestError{Message: "invalid conversationPatch.archived: must be a boolean"}
		}
		result.archived = &b
		hasMutation = true
	}

	// Validate metadata if present.
	if metadataRaw, ok := rawMap["metadata"]; ok {
		metadataTrimmed := bytes.TrimSpace(metadataRaw)
		if !bytes.Equal(metadataTrimmed, []byte("null")) {
			var err error
			result.metadataPatch, err = registrystore.DecodeMetadataPatch(metadataRaw)
			if err != nil {
				return nil, err
			}
			if len(result.metadataPatch) > 0 {
				result.metadataPresent = true
				hasMutation = true
			}
		}
	}

	if !hasMutation {
		return nil, nil
	}

	return result, nil
}

// needsUnarchiveBeforeWrite returns true if the validated patch requests unarchiving (archived=false).
func (v *validatedConversationPatch) needsUnarchiveBeforeWrite() bool {
	return v != nil && v.archived != nil && !*v.archived
}

func (v *validatedConversationPatch) withoutArchived() *validatedConversationPatch {
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

func (v *validatedConversationPatch) changesAgainst(conversation *registrystore.ConversationDetail) *validatedConversationPatch {
	if v == nil || conversation == nil {
		return v
	}
	changes := &validatedConversationPatch{}
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

// validateConversationPatchForOwnerOnly checks if the patch requires owner-level
// authorization (i.e., it includes the archived field).
func validateConversationPatchForOwnerOnly(patch *validatedConversationPatch, hasOwnerAccess bool) error {
	if patch != nil && patch.archived != nil && !hasOwnerAccess {
		return &registrystore.ForbiddenError{}
	}
	return nil
}
