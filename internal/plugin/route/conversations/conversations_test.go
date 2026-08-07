package conversations

import (
	"testing"

	registrystore "github.com/chirino/memory-service/internal/registry/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToConversationSummaryNormalizesNilMetadata(t *testing.T) {
	response := toConversationSummary(registrystore.ConversationSummary{})
	require.NotNil(t, response.Metadata)
	assert.Empty(t, response.Metadata)
}
