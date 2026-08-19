package episodic

import (
	"context"
	"strings"
	"testing"
)

// TestIntersectKindSelectors covers every rule documented on IntersectKindSelectors.
func TestIntersectKindSelectors(t *testing.T) {
	tests := []struct {
		name      string
		callerSel string
		policySel string
		wantSel   string
		wantEmpty bool
	}{
		// Both empty
		{name: "both_empty", callerSel: "", policySel: "", wantSel: "", wantEmpty: false},

		// One side empty
		{name: "caller_empty_policy_family", callerSel: "", policySel: "cognition", wantSel: "cognition", wantEmpty: false},
		{name: "caller_empty_policy_exact", callerSel: "", policySel: "cognition/v2", wantSel: "cognition/v2", wantEmpty: false},
		{name: "caller_family_policy_empty", callerSel: "cognition", policySel: "", wantSel: "cognition", wantEmpty: false},
		{name: "caller_exact_policy_empty", callerSel: "cognition/v2", policySel: "", wantSel: "cognition/v2", wantEmpty: false},

		// Equal
		{name: "equal_exact", callerSel: "default/v1", policySel: "default/v1", wantSel: "default/v1", wantEmpty: false},
		{name: "equal_family", callerSel: "default", policySel: "default", wantSel: "default", wantEmpty: false},

		// Exact + exact disjoint
		{name: "exact_exact_disjoint", callerSel: "default/v1", policySel: "cognition/v2", wantEmpty: true},
		{name: "exact_exact_same_family_disjoint", callerSel: "default/v1", policySel: "default/v2", wantEmpty: true},

		// Exact caller + family policy
		{name: "exact_within_family", callerSel: "default/v1", policySel: "default", wantSel: "default/v1", wantEmpty: false},
		{name: "exact_outside_family", callerSel: "cognition/v2", policySel: "default", wantEmpty: true},

		// Family caller + exact policy
		{name: "family_exact_within", callerSel: "default", policySel: "default/v1", wantSel: "default/v1", wantEmpty: false},
		{name: "family_exact_outside", callerSel: "cognition", policySel: "default/v1", wantEmpty: true},

		// Both family disjoint
		{name: "family_family_disjoint", callerSel: "default", policySel: "cognition", wantEmpty: true},

		// Whitespace trimming
		{name: "whitespace_trimmed", callerSel: "  default/v1  ", policySel: "  default  ", wantSel: "default/v1", wantEmpty: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IntersectKindSelectors(tc.callerSel, tc.policySel)
			if got.Empty != tc.wantEmpty {
				t.Errorf("IntersectKindSelectors(%q, %q).Empty = %v; want %v", tc.callerSel, tc.policySel, got.Empty, tc.wantEmpty)
			}
			if !tc.wantEmpty && got.Selector != tc.wantSel {
				t.Errorf("IntersectKindSelectors(%q, %q).Selector = %q; want %q", tc.callerSel, tc.policySel, got.Selector, tc.wantSel)
			}
		})
	}
}

// TestValidateKindSelector covers valid and invalid selector strings.
func TestValidateKindSelector(t *testing.T) {
	valid := []string{
		"",
		"default",
		"default/v1",
		"cognition",
		"cognition/v2",
		"my-kind/v3",
		"a",
		"a/b",
	}
	for _, s := range valid {
		if err := ValidateKindSelector(s); err != nil {
			t.Errorf("ValidateKindSelector(%q) unexpected error: %v", s, err)
		}
	}

	invalid := []string{
		"/",
		"/v1",
		"default/",
		"Default/v1",   // uppercase
		"default/V1",   // uppercase version
		"123/v1",       // starts with digit
		"default/v1/x", // too many slashes
		"bad kind",     // space
		"bad.kind",     // dot
	}
	for _, s := range invalid {
		if err := ValidateKindSelector(s); err == nil {
			t.Errorf("ValidateKindSelector(%q) expected error but got nil", s)
		}
	}
}

// TestInjectFilterPartsWithKind verifies that the kind intersection is applied
// and that input.kind is passed to the policy (built-in default returns it
// unchanged — no kind output means ki.Selector == callerKind, ki.Empty == false).
func TestInjectFilterPartsWithKind(t *testing.T) {
	ctx := context.Background()
	engine, err := NewPolicyEngine(ctx, "")
	if err != nil {
		t.Fatalf("NewPolicyEngine: %v", err)
	}

	pc := PolicyContext{
		UserID:   "alice",
		ClientID: "agent-1",
		JWTClaims: map[string]interface{}{
			"roles": []string{},
		},
	}

	t.Run("caller_kind_preserved_when_policy_has_no_kind_output", func(t *testing.T) {
		_, _, ki, err := engine.InjectFilterPartsWithKind(ctx,
			[]string{"user", "alice"}, nil, "cognition/v2", pc)
		if err != nil {
			t.Fatalf("InjectFilterPartsWithKind: %v", err)
		}
		// Built-in policy does not output "kind" → policy kind = "" → intersection = callerKind.
		if ki.Empty {
			t.Errorf("ki.Empty = true; want false")
		}
		if ki.Selector != "cognition/v2" {
			t.Errorf("ki.Selector = %q; want %q", ki.Selector, "cognition/v2")
		}
	})

	t.Run("empty_caller_kind_and_no_policy_kind_output_stays_empty", func(t *testing.T) {
		_, _, ki, err := engine.InjectFilterPartsWithKind(ctx,
			[]string{"user", "alice"}, nil, "", pc)
		if err != nil {
			t.Fatalf("InjectFilterPartsWithKind: %v", err)
		}
		if ki.Empty {
			t.Errorf("ki.Empty = true; want false")
		}
		if ki.Selector != "" {
			t.Errorf("ki.Selector = %q; want empty", ki.Selector)
		}
	})
}

// TestInjectFilterPartsWithKindPolicyOutput verifies that a custom filter.rego
// that outputs a "kind" field causes kind intersection to be applied.
func TestInjectFilterPartsWithKindPolicyOutput(t *testing.T) {
	ctx := context.Background()

	// Build a policy that outputs kind = "cognition" (family restriction).
	customFilterRego := `
package memories.filter

namespace_prefix := input.namespace_prefix
attribute_filter := {}
kind := "cognition"
`
	engine := &PolicyEngine{}
	engine.authzSrc = defaultAuthzRego
	engine.filterSrc = customFilterRego
	var loadErr error
	engine.authz, loadErr = prepareQuery(ctx, defaultAuthzRego, "data.memories.authz.decision")
	if loadErr != nil {
		t.Fatalf("prepare authz: %v", loadErr)
	}
	engine.filterInject, loadErr = prepareQuery(ctx, customFilterRego, "data.memories.filter")
	if loadErr != nil {
		t.Fatalf("prepare filterInject: %v", loadErr)
	}

	pc := PolicyContext{UserID: "alice", JWTClaims: map[string]interface{}{"roles": []string{}}}

	t.Run("caller_exact_within_policy_family_uses_exact", func(t *testing.T) {
		_, _, ki, err := engine.InjectFilterPartsWithKind(ctx,
			[]string{"user", "alice"}, nil, "cognition/v2", pc)
		if err != nil {
			t.Fatalf("InjectFilterPartsWithKind: %v", err)
		}
		// callerSel = "cognition/v2", policySel = "cognition" → exact within family
		if ki.Empty {
			t.Errorf("ki.Empty = true; want false")
		}
		if ki.Selector != "cognition/v2" {
			t.Errorf("ki.Selector = %q; want %q", ki.Selector, "cognition/v2")
		}
	})

	t.Run("caller_empty_applies_policy_family", func(t *testing.T) {
		_, _, ki, err := engine.InjectFilterPartsWithKind(ctx,
			[]string{"user", "alice"}, nil, "", pc)
		if err != nil {
			t.Fatalf("InjectFilterPartsWithKind: %v", err)
		}
		// callerSel = "", policySel = "cognition" → policy applies
		if ki.Empty {
			t.Errorf("ki.Empty = true; want false")
		}
		if ki.Selector != "cognition" {
			t.Errorf("ki.Selector = %q; want %q", ki.Selector, "cognition")
		}
	})

	t.Run("caller_disjoint_family_produces_empty", func(t *testing.T) {
		_, _, ki, err := engine.InjectFilterPartsWithKind(ctx,
			[]string{"user", "alice"}, nil, "default", pc)
		if err != nil {
			t.Fatalf("InjectFilterPartsWithKind: %v", err)
		}
		// callerSel = "default", policySel = "cognition" → disjoint families
		if !ki.Empty {
			t.Errorf("ki.Empty = false; want true (disjoint)")
		}
	})
}

// TestInjectFilterPartsWithKindMalformedPolicyOutput verifies that a non-string
// or malformed kind output from filter.rego returns an internal error.
func TestInjectFilterPartsWithKindMalformedPolicyOutput(t *testing.T) {
	ctx := context.Background()

	// Build a policy that outputs kind as an integer (invalid type).
	badTypeRego := `
package memories.filter

namespace_prefix := input.namespace_prefix
attribute_filter := {}
kind := 42
`
	engine := &PolicyEngine{}
	engine.authzSrc = defaultAuthzRego
	engine.filterSrc = badTypeRego
	var loadErr error
	engine.authz, loadErr = prepareQuery(ctx, defaultAuthzRego, "data.memories.authz.decision")
	if loadErr != nil {
		t.Fatalf("prepare authz: %v", loadErr)
	}
	engine.filterInject, loadErr = prepareQuery(ctx, badTypeRego, "data.memories.filter")
	if loadErr != nil {
		t.Fatalf("prepare filterInject: %v", loadErr)
	}

	pc := PolicyContext{UserID: "alice", JWTClaims: map[string]interface{}{"roles": []string{}}}

	_, _, _, err := engine.InjectFilterPartsWithKind(ctx,
		[]string{"user", "alice"}, nil, "default", pc)
	if err == nil {
		t.Fatal("expected error for non-string kind output, got nil")
	}
	if !strings.Contains(err.Error(), "must be a non-empty string") {
		t.Errorf("error = %q; want 'must be a non-empty string' in message", err.Error())
	}
}

// TestInjectFilterPartsWithKindMalformedStringPolicyOutput verifies that a
// malformed string kind output (valid Go type but invalid kind format) returns an error.
func TestInjectFilterPartsWithKindMalformedStringPolicyOutput(t *testing.T) {
	ctx := context.Background()

	// Build a policy that outputs kind as a malformed string (has uppercase).
	badStringRego := `
package memories.filter

namespace_prefix := input.namespace_prefix
attribute_filter := {}
kind := "INVALID-Kind"
`
	engine := &PolicyEngine{}
	engine.authzSrc = defaultAuthzRego
	engine.filterSrc = badStringRego
	var loadErr error
	engine.authz, loadErr = prepareQuery(ctx, defaultAuthzRego, "data.memories.authz.decision")
	if loadErr != nil {
		t.Fatalf("prepare authz: %v", loadErr)
	}
	engine.filterInject, loadErr = prepareQuery(ctx, badStringRego, "data.memories.filter")
	if loadErr != nil {
		t.Fatalf("prepare filterInject: %v", loadErr)
	}

	pc := PolicyContext{UserID: "alice", JWTClaims: map[string]interface{}{"roles": []string{}}}

	_, _, _, err := engine.InjectFilterPartsWithKind(ctx,
		[]string{"user", "alice"}, nil, "default", pc)
	if err == nil {
		t.Fatal("expected error for malformed string kind output, got nil")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Errorf("error = %q; want 'malformed' in message", err.Error())
	}
}

// TestInjectFilterPartsWithKindEmptyStringOutput verifies that a filter.rego
// that outputs kind = "" (present but empty) is a policy error.
// To express "no kind restriction" the key must be absent entirely.
func TestInjectFilterPartsWithKindEmptyStringOutput(t *testing.T) {
	ctx := context.Background()

	// Build a policy that outputs kind as an empty string.
	emptyKindRego := `
package memories.filter

namespace_prefix := input.namespace_prefix
attribute_filter := {}
kind := ""
`
	engine := &PolicyEngine{}
	engine.authzSrc = defaultAuthzRego
	engine.filterSrc = emptyKindRego
	var loadErr error
	engine.authz, loadErr = prepareQuery(ctx, defaultAuthzRego, "data.memories.authz.decision")
	if loadErr != nil {
		t.Fatalf("prepare authz: %v", loadErr)
	}
	engine.filterInject, loadErr = prepareQuery(ctx, emptyKindRego, "data.memories.filter")
	if loadErr != nil {
		t.Fatalf("prepare filterInject: %v", loadErr)
	}

	pc := PolicyContext{UserID: "alice", JWTClaims: map[string]interface{}{"roles": []string{}}}

	_, _, _, err := engine.InjectFilterPartsWithKind(ctx,
		[]string{"user", "alice"}, nil, "default", pc)
	if err == nil {
		t.Fatal("expected error for empty-string kind output, got nil")
	}
	if !strings.Contains(err.Error(), "non-empty when present") {
		t.Errorf("error = %q; want 'non-empty when present' in message", err.Error())
	}
}
