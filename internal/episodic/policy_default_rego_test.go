package episodic

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/open-policy-agent/opa/v1/rego"
)

const defaultPolicyAssertionsRego = `
package memories.tests

# --- authz assertions ---

test_allow_owner_namespace if {
	data.memories.authz.decision with input as {
		"operation": "write",
		"namespace": ["user", "alice", "prefs"],
		"key": "theme",
		"value": {"locale": "en"},
		"index": {"locale": "en"},
		"context": {
			"user_id": "alice",
			"client_id": "agent-1",
			"jwt_claims": {"roles": []}
		}
	} == {"allow": true}
}

test_deny_other_subject if {
	data.memories.authz.decision with input as {
		"operation": "read",
		"namespace": ["user", "bob", "prefs"],
		"key": "theme",
		"context": {
			"user_id": "alice",
			"client_id": "agent-1",
			"jwt_claims": {"roles": []}
		}
	} == {"allow": false, "reason": "access denied"}
}

test_deny_non_user_namespace if {
	data.memories.authz.decision with input as {
		"operation": "write",
		"namespace": ["org", "alice", "prefs"],
		"key": "theme",
		"value": {"locale": "en"},
		"index": {"locale": "en"},
		"context": {
			"user_id": "alice",
			"client_id": "agent-1",
			"jwt_claims": {"roles": []}
		}
	} == {"allow": false, "reason": "access denied"}
}

test_deny_owner_when_too_many_index_keys if {
	data.memories.authz.decision with input as {
		"operation": "write",
		"namespace": ["user", "alice", "prefs"],
		"key": "theme",
		"value": {"a": "x", "b": "y"},
		"index": {"a": "x", "b": "y", "c": "z", "d": "w", "e": "v", "f": "u", "g": "t", "h": "s", "i": "r"},
		"context": {
			"user_id": "alice",
			"client_id": "agent-1",
			"jwt_claims": {"roles": []}
		}
	} == {"allow": false, "reason": "too many index fields (max 8)"}
}

# --- attribute extraction assertions ---

test_extracts_namespace_and_sub if {
	data.memories.attributes.attributes with input as {
		"namespace": ["user", "alice", "notes"],
		"key": "k1",
		"value": {"text": "hello"},
		"index": {"text": "hello"},
		"context": {
			"user_id": "alice",
			"client_id": "agent-1",
			"jwt_claims": {"roles": []}
		}
	} == {"namespace": "user", "sub": "alice"}
}

# --- filter injection assertions ---

test_filter_narrows_prefix_to_subject if {
	data.memories.filter with input as {
		"namespace_prefix": ["user"],
		"filter": {},
		"context": {
			"user_id": "alice",
			"jwt_claims": {"roles": []}
		}
	} == {
		"namespace_prefix": ["user", "alice"],
		"attribute_filter": {"namespace": "user", "sub": "alice"}
	}
}

test_filter_keeps_narrower_prefix if {
	data.memories.filter with input as {
		"namespace_prefix": ["user", "alice", "notes"],
		"filter": {},
		"context": {
			"user_id": "alice",
			"jwt_claims": {"roles": []}
		}
	} == {
		"namespace_prefix": ["user", "alice", "notes"],
		"attribute_filter": {"namespace": "user", "sub": "alice"}
	}
}

test_filter_enforces_namespace_and_sub_attributes if {
	data.memories.filter with input as {
		"namespace_prefix": ["user", "alice"],
		"filter": {"topic": "python"},
		"context": {
			"user_id": "alice",
			"jwt_claims": {"roles": []}
		}
	} == {
		"namespace_prefix": ["user", "alice"],
		"attribute_filter": {"namespace": "user", "sub": "alice"}
	}
}

test_admin_filter_not_restricted if {
	data.memories.filter with input as {
		"namespace_prefix": ["user"],
		"filter": {},
		"context": {
			"user_id": "alice",
			"jwt_claims": {"roles": ["admin"]}
		}
	} == {
		"namespace_prefix": ["user"],
		"attribute_filter": {}
	}
}
`

func TestDefaultPoliciesRegoAssertions(t *testing.T) {
	modules := map[string]string{
		"authz.rego":      defaultAuthzRego,
		"attributes.rego": defaultAttrExtractRego,
		"filter.rego":     defaultFilterInjectRego,
		"tests.rego":      defaultPolicyAssertionsRego,
	}
	testRules := []string{
		"test_allow_owner_namespace",
		"test_deny_other_subject",
		"test_deny_non_user_namespace",
		"test_deny_owner_when_too_many_index_keys",
		"test_extracts_namespace_and_sub",
		"test_filter_narrows_prefix_to_subject",
		"test_filter_keeps_narrower_prefix",
		"test_filter_enforces_namespace_and_sub_attributes",
		"test_admin_filter_not_restricted",
	}

	for _, rule := range testRules {
		t.Run(rule, func(t *testing.T) {
			query := fmt.Sprintf("data.memories.tests.%s", rule)
			if !evalRegoBoolean(t, modules, query) {
				t.Fatalf("rego assertion failed: %s", query)
			}
		})
	}
}

func TestCognitionPoliciesCompileWithRegoV1(t *testing.T) {
	policyDir := filepath.Join("..", "..", "deploy", "episodic-policies", "cognition")
	_, err := NewPolicyEngine(context.Background(), policyDir)
	if err != nil {
		t.Fatalf("compile cognition policies: %v", err)
	}
}

const testTimestamp = "2025-06-10T13:30:00Z"

func TestCognitionPoliciesRegoAssertions(t *testing.T) {
	policyDir := filepath.Join("..", "..", "deploy", "episodic-policies", "cognition")
	engine, err := NewPolicyEngine(context.Background(), policyDir)
	if err != nil {
		t.Fatalf("compile cognition policies: %v", err)
	}

	ctx := context.Background()

	t.Run("extracts_observedAt_and_effectiveAt_from_index", func(t *testing.T) {
		namespace := []string{"user", "alice", "cognition.v1", "fact"}
		value := map[string]interface{}{
			"content":    "User prefers Go",
			"confidence": 0.9,
		}
		index := map[string]string{
			"content":      "User prefers Go",
			"type":         "fact",
			"observed_at":  testTimestamp,
			"effective_at": testTimestamp,
		}
		attrs, err := engine.ExtractAttributes(ctx, namespace, "key-1", value, index, PolicyContext{})
		if err != nil {
			t.Fatalf("ExtractAttributes: %v", err)
		}
		if got, ok := attrs["observedAt"]; !ok || got != testTimestamp {
			t.Errorf("observedAt: want %q, got %v", testTimestamp, got)
		}
		if got, ok := attrs["effectiveAt"]; !ok || got != testTimestamp {
			t.Errorf("effectiveAt: want %q, got %v", testTimestamp, got)
		}
	})

	t.Run("omits_observedAt_when_index_key_absent", func(t *testing.T) {
		namespace := []string{"user", "alice", "cognition.v1", "fact"}
		value := map[string]interface{}{"content": "old memory"}
		index := map[string]string{"content": "old memory", "type": "fact"}
		attrs, err := engine.ExtractAttributes(ctx, namespace, "key-2", value, index, PolicyContext{})
		if err != nil {
			t.Fatalf("ExtractAttributes: %v", err)
		}
		if _, ok := attrs["observedAt"]; ok {
			t.Error("observedAt must not be present when missing from index")
		}
	})

	t.Run("omits_observedAt_when_index_value_is_empty", func(t *testing.T) {
		namespace := []string{"user", "alice", "cognition.v1", "fact"}
		value := map[string]interface{}{"content": "some fact"}
		index := map[string]string{"observed_at": "", "effective_at": ""}
		attrs, err := engine.ExtractAttributes(ctx, namespace, "key-3", value, index, PolicyContext{})
		if err != nil {
			t.Fatalf("ExtractAttributes: %v", err)
		}
		if _, ok := attrs["observedAt"]; ok {
			t.Error("observedAt must not be present when index value is empty string")
		}
		if _, ok := attrs["effectiveAt"]; ok {
			t.Error("effectiveAt must not be present when index value is empty string")
		}
	})
}

func evalRegoBoolean(t *testing.T, modules map[string]string, query string) bool {
	t.Helper()
	opts := []func(*rego.Rego){rego.Query(query)}
	for name, src := range modules {
		opts = append(opts, rego.Module(name, src))
	}

	r := rego.New(opts...)
	results, err := r.Eval(context.Background())
	if err != nil {
		t.Fatalf("eval %s: %v", query, err)
	}
	if len(results) == 0 || len(results[0].Expressions) == 0 {
		t.Fatalf("eval %s: no result", query)
	}
	v, ok := results[0].Expressions[0].Value.(bool)
	if !ok {
		t.Fatalf("eval %s: expected bool, got %T", query, results[0].Expressions[0].Value)
	}
	return v
}
