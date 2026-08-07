package entries

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateConversationPatchMetadataNoOps(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`{"metadata":null}`,
		`{"metadata":{}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			patch, err := validateConversationPatch(json.RawMessage(raw))
			require.NoError(t, err)
			assert.Nil(t, patch)
		})
	}
}

func TestValidateConversationPatchMetadataMutation(t *testing.T) {
	patch, err := validateConversationPatch(json.RawMessage(`{"metadata":{"a.b":"value","delete":null}}`))
	require.NoError(t, err)
	require.NotNil(t, patch)
	assert.True(t, patch.metadataPresent)
	assert.Equal(t, "value", patch.metadataPatch["a.b"])
	assert.Contains(t, patch.metadataPatch, "delete")
	assert.Nil(t, patch.metadataPatch["delete"])
}
