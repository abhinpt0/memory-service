package cucumber

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRewriteRequestPathPreservesQueryShape(t *testing.T) {
	s := &TestScenario{
		ScenarioUID: "test123",
		userAliases: map[string]string{"alice": "alice-test123"},
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"missing equals", "/v1/conversations?metadata[status]", "/v1/conversations?metadata%5Bstatus%5D"},
		{"explicit empty", "/v1/conversations?metadata[status]=", "/v1/conversations?metadata%5Bstatus%5D="},
		{"repeated mixed order", "/v1/conversations?tag&tag=value", "/v1/conversations?tag&tag=value"},
		{"repeated reverse mixed order", "/v1/conversations?tag=value&tag", "/v1/conversations?tag=value&tag"},
		{"repeated metadata mixed order", "/v1/conversations?metadata[status]&metadata[status]=active", "/v1/conversations?metadata%5Bstatus%5D&metadata%5Bstatus%5D=active"},
		{"encoded metadata brackets", "/v1/conversations?metadata%5Bkey%5D=value", "/v1/conversations?metadata%5Bkey%5D=value"},
		{"equals in value", "/v1/conversations?filter=key=value", "/v1/conversations?filter=key%3Dvalue"},
		{"user isolation", "/v1/conversations?userId=alice", "/v1/conversations?userId=alice-test123"},
		{"query order", "/v1/conversations?z=last&a=first", "/v1/conversations?z=last&a=first"},
		{"path user isolation", "/v1/users/alice/conversations", "/v1/users/alice-test123/conversations"},
		{"no query", "/v1/conversations", "/v1/conversations"},
		{"force query", "/v1/conversations?", "/v1/conversations?"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.expected, s.RewriteRequestPath(test.input))
		})
	}
}

func TestRewriteRequestPathPreservesInvalidURL(t *testing.T) {
	s := &TestScenario{
		ScenarioUID: "test123",
		userAliases: map[string]string{"alice": "alice-test123"},
	}

	assert.Equal(t, "://invalid-url", s.RewriteRequestPath("://invalid-url"))
}
