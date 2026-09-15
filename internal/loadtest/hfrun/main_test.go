package main

import "testing"

func TestCheckStatsRejectsMissingStatistics(t *testing.T) {
	tests := []struct {
		name string
		raw  map[string]any
	}{
		{name: "missing field", raw: map[string]any{}},
		{name: "empty array", raw: map[string]any{"statistics": []any{}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := checkStats(tt.raw, "list-forks"); err == nil {
				t.Fatal("checkStats accepted a result without statistics")
			}
		})
	}
}
