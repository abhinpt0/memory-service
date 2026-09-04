package store

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestDecodeMetadataPatch_ExplicitNull(t *testing.T) {
	patch := []byte(`{"key1": null, "key2": "value"}`)
	result, err := DecodeMetadataPatch(patch)
	require.NoError(t, err)
	assert.Len(t, result, 2)
	assert.Nil(t, result["key1"])
	assert.Equal(t, "value", result["key2"])
}

func TestMetadataPatchChangesAgainstDropsNoOps(t *testing.T) {
	patch := MetadataPatch{
		"same":   "value",
		"change": "new",
		"delete": nil,
		"absent": nil,
	}
	changes := patch.ChangesAgainst(map[string]interface{}{
		"same":   "value",
		"change": "old",
		"delete": "present",
	})

	require.Equal(t, MetadataPatch{"change": "new", "delete": nil}, changes)
}

func TestMetadataPatchChangesAgainstDropsNestedBSONNoOps(t *testing.T) {
	patch, err := DecodeMetadataPatch([]byte(`{
		"state":{"status":"done","steps":[1.0,2e0]},
		"reviewers":[{"name":"alice","roles":["owner","editor"]}]
	}`))
	require.NoError(t, err)

	current := map[string]interface{}{
		"state": bson.D{
			{Key: "status", Value: "done"},
			{Key: "steps", Value: bson.A{int32(1), int64(2)}},
		},
		"reviewers": bson.A{
			bson.D{
				{Key: "name", Value: "alice"},
				{Key: "roles", Value: bson.A{"owner", "editor"}},
			},
		},
	}

	require.Nil(t, patch.ChangesAgainst(current))
}

func TestDecodeMetadataPatch_NestedReplacement(t *testing.T) {
	patch := []byte(`{"nested": {"inner": "value", "num": 42}}`)
	result, err := DecodeMetadataPatch(patch)
	require.NoError(t, err)
	assert.Len(t, result, 1)
	nested, ok := result["nested"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "value", nested["inner"])
	assert.Equal(t, float64(42), nested["num"])
}

func TestDecodeMetadataPatch_NullPatch(t *testing.T) {
	patch := []byte(`null`)
	result, err := DecodeMetadataPatch(patch)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestDecodeMetadataPatch_EmptyPatch(t *testing.T) {
	patch := []byte(``)
	result, err := DecodeMetadataPatch(patch)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestDecodeMetadataPatch_MalformedJSON(t *testing.T) {
	patch := []byte(`{invalid}`)
	_, err := DecodeMetadataPatch(patch)
	assert.Error(t, err)
}

func TestDecodeMetadataPatch_NonObjectInput(t *testing.T) {
	patch := []byte(`["array"]`)
	_, err := DecodeMetadataPatch(patch)
	assert.Error(t, err)
}

func TestDecodeMetadataPatch_MalformedValue(t *testing.T) {
	// Invalid JSON in a value should still error
	patch := []byte(`{"key": invalid}`)
	_, err := DecodeMetadataPatch(patch)
	assert.Error(t, err)
}

func TestMetadataFilterFromOptionalPair_BothNil(t *testing.T) {
	filter, err := MetadataFilterFromOptionalPair(nil, nil)
	require.NoError(t, err)
	assert.Nil(t, filter)
}

func TestMetadataFilterFromOptionalPair_KeyOnly(t *testing.T) {
	key := "status"
	filter, err := MetadataFilterFromOptionalPair(&key, nil)
	assert.Error(t, err)
	assert.Nil(t, filter)
	assert.Contains(t, err.Error(), "value is required")
}

func TestMetadataFilterFromOptionalPair_ValueOnly(t *testing.T) {
	value := "waiting"
	filter, err := MetadataFilterFromOptionalPair(nil, &value)
	assert.Error(t, err)
	assert.Nil(t, filter)
	assert.Contains(t, err.Error(), "key is required")
}

func TestMetadataFilterFromOptionalPair_EmptyKey(t *testing.T) {
	key := ""
	value := "waiting"
	filter, err := MetadataFilterFromOptionalPair(&key, &value)
	assert.Error(t, err)
	assert.Nil(t, filter)
	assert.Contains(t, err.Error(), "invalid")
}

func TestMetadataFilterFromOptionalPair_InvalidKey(t *testing.T) {
	key := "bad key!"
	value := "waiting"
	filter, err := MetadataFilterFromOptionalPair(&key, &value)
	assert.Error(t, err)
	assert.Nil(t, filter)
	assert.Contains(t, err.Error(), "invalid")
}

func TestMetadataFilterFromOptionalPair_ValidPair(t *testing.T) {
	key := "status"
	value := "waiting"
	filter, err := MetadataFilterFromOptionalPair(&key, &value)
	require.NoError(t, err)
	require.NotNil(t, filter)
	assert.Equal(t, "status", filter.Key)
	assert.Equal(t, "waiting", filter.Value)
}

func TestMetadataFilterFromOptionalPair_EmptyValue(t *testing.T) {
	key := "status"
	value := ""
	filter, err := MetadataFilterFromOptionalPair(&key, &value)
	require.NoError(t, err)
	require.NotNil(t, filter)
	assert.Equal(t, "status", filter.Key)
	assert.Equal(t, "", filter.Value)
}

func TestParseMetadataFilterQuery_NoFilter(t *testing.T) {
	values := url.Values{}
	values.Set("mode", "all")
	values.Set("limit", "20")
	filter, err := ParseMetadataFilterQuery(values.Encode())
	require.NoError(t, err)
	assert.Nil(t, filter)
}

func TestParseMetadataFilterQuery_ValidFilter(t *testing.T) {
	values := url.Values{}
	values.Set("metadata[status]", "waiting")
	filter, err := ParseMetadataFilterQuery(values.Encode())
	require.NoError(t, err)
	require.NotNil(t, filter)
	assert.Equal(t, "status", filter.Key)
	assert.Equal(t, "waiting", filter.Value)
}

func TestParseMetadataFilterQuery_EmptyValue(t *testing.T) {
	values := url.Values{}
	values.Set("metadata[status]", "")
	filter, err := ParseMetadataFilterQuery(values.Encode())
	require.NoError(t, err)
	require.NotNil(t, filter)
	assert.Equal(t, "status", filter.Key)
	assert.Equal(t, "", filter.Value)
}

func TestParseMetadataFilterQuery_MultipleKeys(t *testing.T) {
	values := url.Values{}
	values.Set("metadata[status]", "waiting")
	values.Set("metadata[priority]", "high")
	filter, err := ParseMetadataFilterQuery(values.Encode())
	assert.Error(t, err)
	assert.Nil(t, filter)
	assert.Contains(t, err.Error(), "exactly one key")
}

func TestParseMetadataFilterQuery_RepeatedValue(t *testing.T) {
	values := url.Values{}
	values.Add("metadata[status]", "waiting")
	values.Add("metadata[status]", "running")
	filter, err := ParseMetadataFilterQuery(values.Encode())
	assert.Error(t, err)
	assert.Nil(t, filter)
	assert.Contains(t, err.Error(), "exactly one value")
}

func TestParseMetadataFilterQuery_InvalidKey(t *testing.T) {
	values := url.Values{}
	values.Set("metadata[bad key!]", "value")
	filter, err := ParseMetadataFilterQuery(values.Encode())
	assert.Error(t, err)
	assert.Nil(t, filter)
	assert.Contains(t, err.Error(), "invalid")
}

func TestParseMetadataFilterQuery_EmptyKey(t *testing.T) {
	values := url.Values{}
	values.Set("metadata[]", "value")
	filter, err := ParseMetadataFilterQuery(values.Encode())
	assert.Error(t, err)
	assert.Nil(t, filter)
}

func TestParseMetadataFilterQuery_BareMetadata(t *testing.T) {
	values := url.Values{}
	values.Set("metadata", "value")
	filter, err := ParseMetadataFilterQuery(values.Encode())
	assert.Error(t, err)
	assert.Nil(t, filter)
	assert.Contains(t, err.Error(), "invalid metadata filter parameter")
}

func TestParseMetadataFilterQuery_MalformedBracket(t *testing.T) {
	values := url.Values{}
	values.Set("metadata[missing-close", "value")
	filter, err := ParseMetadataFilterQuery(values.Encode())
	assert.Error(t, err)
	assert.Nil(t, filter)
}

func TestParseMetadataFilterQuery_MalformedMetadataPrefix(t *testing.T) {
	values := url.Values{}
	values.Set("metadataFoo", "bar")
	values.Set("mode", "all")
	filter, err := ParseMetadataFilterQuery(values.Encode())
	assert.Error(t, err)
	assert.Nil(t, filter)
}

func TestParseMetadataFilterQuery_MixedValidAndMalformed(t *testing.T) {
	values := url.Values{}
	values.Set("metadata[status]", "waiting")
	values.Set("metadataFoo", "bar")
	values.Set("mode", "all")
	filter, err := ParseMetadataFilterQuery(values.Encode())
	assert.Error(t, err)
	assert.Nil(t, filter)
}

func TestParseMetadataFilterQuery_MissingEquals(t *testing.T) {
	const rawQuery = "metadata[status]"
	filter, err := ParseMetadataFilterQuery(rawQuery)
	assert.Error(t, err)
	assert.Nil(t, filter)
}

func TestParseMetadataFilterQuery_ExplicitEmptyValue(t *testing.T) {
	const rawQuery = "metadata[status]="
	filter, err := ParseMetadataFilterQuery(rawQuery)
	require.NoError(t, err)
	require.NotNil(t, filter)
	assert.Equal(t, "status", filter.Key)
	assert.Empty(t, filter.Value)
}

func TestParseMetadataFilterQuery_EncodedMissingEquals(t *testing.T) {
	const rawQuery = "metadata%5Bstatus%5D"
	filter, err := ParseMetadataFilterQuery(rawQuery)
	assert.Error(t, err)
	assert.Nil(t, filter)
}

func TestParseMetadataFilterQuery_MalformedEncodedValue(t *testing.T) {
	filter, err := ParseMetadataFilterQuery("metadata%5Bstatus%5D=%ZZ")
	assert.Error(t, err)
	assert.Nil(t, filter)
}
