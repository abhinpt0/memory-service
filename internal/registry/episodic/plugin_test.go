package episodic

import "testing"

func TestNormalizeAttributeFiltersAcceptsPushdownOperators(t *testing.T) {
	filter, err := NormalizeAttributeFilters(map[string]interface{}{
		"tenant":     "acme",
		"project":    []interface{}{"alpha", "beta"},
		"created_at": map[string]interface{}{"$gte": "2026-01-02T03:04:05Z"},
		"score":      map[string]interface{}{"$lte": 10},
		"tag":        map[string]interface{}{"$exists": true},
	})
	if err != nil {
		t.Fatalf("NormalizeAttributeFilters returned error: %v", err)
	}
	if got, want := len(filter.Conditions), 5; got != want {
		t.Fatalf("condition count = %d, want %d", got, want)
	}
}

func TestNormalizeAttributeFiltersRejectsNonPushdownOperators(t *testing.T) {
	tests := map[string]map[string]interface{}{
		"unknown":      {"tenant": map[string]interface{}{"$unknown": "acme"}},
		"exists false": {"tenant": map[string]interface{}{"$exists": false}},
	}

	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeAttributeFilters(raw); err == nil {
				t.Fatalf("NormalizeAttributeFilters returned nil error")
			}
		})
	}
}

func TestNormalizeAttributeFiltersCombinesCallerAndPolicyConstraints(t *testing.T) {
	filter, err := NormalizeAttributeFilters(
		map[string]interface{}{"tenant": "acme"},
		map[string]interface{}{"tenant": map[string]interface{}{"$in": []interface{}{"acme", "beta"}}},
	)
	if err != nil {
		t.Fatalf("NormalizeAttributeFilters returned error: %v", err)
	}
	if got, want := len(filter.Conditions), 2; got != want {
		t.Fatalf("condition count = %d, want %d", got, want)
	}
}

// TestNormalizeAttributeFiltersTemporalFields verifies that the temporal policy
// attributes emitted by the cognition attributes.rego policy (observedAt,
// effectiveAt) are accepted as valid filter fields with range operators, making
// them queryable through the search API.
func TestNormalizeAttributeFiltersTemporalFields(t *testing.T) {
	t.Run("observedAt_gte", func(t *testing.T) {
		filter, err := NormalizeAttributeFilters(map[string]interface{}{
			"observedAt": map[string]interface{}{"$gte": "2025-06-01T00:00:00Z"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := len(filter.Conditions); got != 1 {
			t.Fatalf("condition count = %d, want 1", got)
		}
		cond := filter.Conditions[0]
		if cond.Field != "observedAt" {
			t.Errorf("field = %q, want %q", cond.Field, "observedAt")
		}
		if cond.Op != AttributeFilterOpGte {
			t.Errorf("op = %q, want %q", cond.Op, AttributeFilterOpGte)
		}
		if cond.RangeKind != AttributeFilterRangeTime {
			t.Errorf("rangeKind = %q, want %q", cond.RangeKind, AttributeFilterRangeTime)
		}
	})

	t.Run("effectiveAt_lte", func(t *testing.T) {
		filter, err := NormalizeAttributeFilters(map[string]interface{}{
			"effectiveAt": map[string]interface{}{"$lte": "2025-12-31T23:59:59Z"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := len(filter.Conditions); got != 1 {
			t.Fatalf("condition count = %d, want 1", got)
		}
		cond := filter.Conditions[0]
		if cond.Field != "effectiveAt" {
			t.Errorf("field = %q, want %q", cond.Field, "effectiveAt")
		}
		if cond.Op != AttributeFilterOpLte {
			t.Errorf("op = %q, want %q", cond.Op, AttributeFilterOpLte)
		}
		if cond.RangeKind != AttributeFilterRangeTime {
			t.Errorf("rangeKind = %q, want %q", cond.RangeKind, AttributeFilterRangeTime)
		}
	})

	t.Run("observedAt_range_window", func(t *testing.T) {
		filter, err := NormalizeAttributeFilters(map[string]interface{}{
			"observedAt": map[string]interface{}{
				"$gte": "2025-01-01T00:00:00Z",
				"$lte": "2025-06-30T23:59:59Z",
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := len(filter.Conditions); got != 2 {
			t.Fatalf("condition count = %d, want 2", got)
		}
		for _, cond := range filter.Conditions {
			if cond.RangeKind != AttributeFilterRangeTime {
				t.Errorf("rangeKind = %q, want %q", cond.RangeKind, AttributeFilterRangeTime)
			}
		}
	})

	t.Run("rejects_non_rfc3339_string", func(t *testing.T) {
		_, err := NormalizeAttributeFilters(map[string]interface{}{
			"observedAt": map[string]interface{}{"$gte": "not-a-timestamp"},
		})
		if err == nil {
			t.Fatal("expected error for non-RFC3339 string, got nil")
		}
	})

	t.Run("observedAt_eq_string", func(t *testing.T) {
		filter, err := NormalizeAttributeFilters(map[string]interface{}{
			"observedAt": "2025-06-10T13:30:00Z",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := len(filter.Conditions); got != 1 {
			t.Fatalf("condition count = %d, want 1", got)
		}
		if filter.Conditions[0].Op != AttributeFilterOpEq {
			t.Errorf("op = %q, want %q", filter.Conditions[0].Op, AttributeFilterOpEq)
		}
	})
}
