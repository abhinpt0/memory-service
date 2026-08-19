package episodic

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
		"attribute_filter": {}
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
		"attribute_filter": {}
	}
}

test_filter_uses_namespace_scope_without_projection_fields if {
	data.memories.filter with input as {
		"namespace_prefix": ["user", "alice"],
		"filter": {"topic": "python"},
		"context": {
			"user_id": "alice",
			"jwt_claims": {"roles": []}
		}
	} == {
		"namespace_prefix": ["user", "alice"],
		"attribute_filter": {}
	}
}

test_admin_role_filter_is_user_scoped if {
	data.memories.filter with input as {
		"namespace_prefix": ["user"],
		"filter": {},
		"context": {
			"user_id": "alice",
			"jwt_claims": {"roles": ["admin"]}
		}
	} == {
		"namespace_prefix": ["user", "alice"],
		"attribute_filter": {}
	}
}

# The default filter policy does not output a "kind" field, so callers retain
# their kind selector unchanged after IntersectKindSelectors("cognition/v2", "").
test_default_policy_does_not_output_kind if {
	result := data.memories.filter with input as {
		"namespace_prefix": ["user", "alice"],
		"filter": {},
		"kind": "cognition/v2",
		"context": {
			"user_id": "alice",
			"jwt_claims": {"roles": []}
		}
	}
	not result.kind
}
`

func TestDefaultPoliciesRegoAssertions(t *testing.T) {
	modules := map[string]string{
		"authz.rego":  defaultAuthzRego,
		"filter.rego": defaultFilterInjectRego,
		"tests.rego":  defaultPolicyAssertionsRego,
	}
	testRules := []string{
		"test_allow_owner_namespace",
		"test_deny_other_subject",
		"test_deny_non_user_namespace",
		"test_deny_owner_when_too_many_index_keys",
		"test_filter_narrows_prefix_to_subject",
		"test_filter_keeps_narrower_prefix",
		"test_filter_uses_namespace_scope_without_projection_fields",
		"test_admin_role_filter_is_user_scoped",
		"test_default_policy_does_not_output_kind",
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

func TestPolicyImportDirectoryAllowsMemoryKindProjectionSubdirectories(t *testing.T) {
	policyImportDir := filepath.Join("..", "..", "deploy", "episodic-policies", "cognition")
	if _, err := NewPolicyEngine(context.Background(), policyImportDir); err != nil {
		t.Fatalf("memory-kind projection in policy import directory should be accepted: %v", err)
	}
}

func TestConfiguredPolicyDirectoryRequiresBothPrograms(t *testing.T) {
	t.Parallel()
	for _, missing := range []string{"authz.rego", "filter.rego"} {
		t.Run(missing, func(t *testing.T) {
			dir := t.TempDir()
			for name, source := range map[string]string{
				"authz.rego":  defaultAuthzRego,
				"filter.rego": defaultFilterInjectRego,
			} {
				if name != missing {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			_, err := NewPolicyEngine(context.Background(), dir)
			if err == nil || !strings.Contains(err.Error(), "requires "+missing) {
				t.Fatalf("expected missing %s error, got %v", missing, err)
			}
		})
	}
}

func TestConfiguredPolicyDirectoryAllowsManifestRegoAssets(t *testing.T) {
	dir := t.TempDir()
	for name, source := range map[string]string{
		"authz.rego":     defaultAuthzRego,
		"filter.rego":    defaultFilterInjectRego,
		"attribute.rego": "package memories.attributes",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NewPolicyEngine(context.Background(), dir); err != nil {
		t.Fatalf("non-global Rego assets should be left for manifest-based importers: %v", err)
	}
}

// testTimestamp is the expected normalised form: fixed-width nanosecond-precision UTC.
const testTimestamp = "2025-06-10T13:30:00.000000000Z"

// testTimestampWithNanos has sub-second precision; after normalisation it must
// equal testTimestampNanosNorm.
const testTimestampWithNanos = "2025-06-10T13:30:00.500000000Z"

// testTimestampNanosNorm is the expected normalised form of testTimestampWithNanos.
const testTimestampNanosNorm = "2025-06-10T13:30:00.500000000Z"

// loadCognitionSchemaProgram compiles the cognition projection.rego as a schema program.
func loadCognitionSchemaProgram(t *testing.T) interface {
	Eval(context.Context, ...interface{}) (interface{}, error)
} {
	t.Helper()
	// Not used directly — we use CompileKindProjection + EvaluateKindProjection instead.
	return nil
}

// TestCognitionSchemaProjection tests the cognition projection.rego compiled as a
// MemoryKindVersion program (Enhancement 115).
func TestCognitionSchemaProjection(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "deploy", "episodic-policies", "cognition", "projection.rego"))
	if err != nil {
		t.Fatalf("read cognition projection.rego: %v", err)
	}
	ctx := context.Background()
	pq, err := CompileKindProjection(ctx, string(src))
	if err != nil {
		t.Fatalf("CompileKindProjection: %v", err)
	}

	t.Run("extracts_observedAt_and_effectiveAt_from_value", func(t *testing.T) {
		namespace := []string{"user", "alice", "cognition.v1", "facts"}
		value := map[string]interface{}{
			"kind":         "fact",
			"statement":    "User prefers Go",
			"confidence":   0.9,
			"observed_at":  testTimestamp,
			"effective_at": testTimestamp,
		}
		index := map[string]string{
			"statement": "user prefers go",
			"title":     "preferred language go",
		}
		attrs, err := EvaluateKindProjection(ctx, pq, namespace, "key-1", value, index)
		if err != nil {
			t.Fatalf("EvaluateKindProjection: %v", err)
		}
		if got, ok := attrs["observedAt"]; !ok || got != testTimestamp {
			t.Errorf("observedAt: want %q, got %v", testTimestamp, got)
		}
		if got, ok := attrs["effectiveAt"]; !ok || got != testTimestamp {
			t.Errorf("effectiveAt: want %q, got %v", testTimestamp, got)
		}
	})

	t.Run("omits_temporal_attributes_when_value_keys_absent", func(t *testing.T) {
		namespace := []string{"user", "alice", "cognition.v1", "facts"}
		value := map[string]interface{}{"kind": "fact", "statement": "old memory"}
		index := map[string]string{"statement": "old memory"}
		attrs, err := EvaluateKindProjection(ctx, pq, namespace, "key-2", value, index)
		if err != nil {
			t.Fatalf("EvaluateKindProjection: %v", err)
		}
		if _, ok := attrs["observedAt"]; ok {
			t.Error("observedAt must not be present when missing from value")
		}
		if _, ok := attrs["effectiveAt"]; ok {
			t.Error("effectiveAt must not be present when missing from value")
		}
	})

	t.Run("omits_temporal_attributes_when_values_are_empty", func(t *testing.T) {
		namespace := []string{"user", "alice", "cognition.v1", "facts"}
		value := map[string]interface{}{
			"kind":         "fact",
			"statement":    "some fact",
			"observed_at":  "",
			"effective_at": "",
		}
		index := map[string]string{"statement": "some fact", "type": "fact"}
		attrs, err := EvaluateKindProjection(ctx, pq, namespace, "key-3", value, index)
		if err != nil {
			t.Fatalf("EvaluateKindProjection: %v", err)
		}
		if _, ok := attrs["observedAt"]; ok {
			t.Error("observedAt must not be present when value is empty string")
		}
		if _, ok := attrs["effectiveAt"]; ok {
			t.Error("effectiveAt must not be present when value is empty string")
		}
	})

	t.Run("omits_temporal_attributes_when_values_are_not_rfc3339", func(t *testing.T) {
		namespace := []string{"user", "alice", "cognition.v1", "facts"}
		value := map[string]interface{}{
			"kind":         "fact",
			"statement":    "some fact",
			"observed_at":  "tomorrow",
			"effective_at": "next week",
		}
		index := map[string]string{"statement": "some fact", "type": "fact"}
		attrs, err := EvaluateKindProjection(ctx, pq, namespace, "key-4", value, index)
		if err != nil {
			t.Fatalf("EvaluateKindProjection: %v", err)
		}
		if _, ok := attrs["observedAt"]; ok {
			t.Error("observedAt must not be present when value is not RFC3339")
		}
		if _, ok := attrs["effectiveAt"]; ok {
			t.Error("effectiveAt must not be present when value is not RFC3339")
		}
	})

	t.Run("normalises_nanosecond_timestamp_to_fixed_width_nanosecond_utc", func(t *testing.T) {
		namespace := []string{"user", "alice", "cognition.v1", "facts"}
		value := map[string]interface{}{
			"kind":        "fact",
			"statement":   "some fact",
			"observed_at": testTimestampWithNanos,
		}
		attrs, err := EvaluateKindProjection(ctx, pq, namespace, "key-6", value, map[string]string{})
		if err != nil {
			t.Fatalf("EvaluateKindProjection: %v", err)
		}
		got, ok := attrs["observedAt"]
		if !ok {
			t.Fatal("observedAt must be present for a valid RFC3339 timestamp with nanos")
		}
		if got != testTimestampNanosNorm {
			t.Errorf("observedAt: want %q, got %q", testTimestampNanosNorm, got)
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
