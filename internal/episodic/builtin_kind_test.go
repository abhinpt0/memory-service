package episodic

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuiltinDefaultKindVersion(t *testing.T) {
	ctx := context.Background()
	kind, err := BuiltinDefaultKindVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, DefaultKindName, kind.Name)
	require.True(t, kind.Writable)
	require.Equal(t, map[string]string{
		"namespace": string(AttributeTypeString),
		"sub":       string(AttributeTypeString),
	}, kind.AttributeTypes)
	require.NotNil(t, kind.AttributesRego)

	query, err := CompileKindProjection(ctx, *kind.AttributesRego)
	require.NoError(t, err)
	attributes, err := EvaluateKindProjection(
		ctx,
		query,
		[]string{"user", "alice", "preferences"},
		"theme",
		map[string]any{"value": "dark"},
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"namespace": "user",
		"sub":       "alice",
	}, attributes)
}
