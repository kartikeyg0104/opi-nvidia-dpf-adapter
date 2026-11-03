package mapping

import "testing"

func TestPrunePolicyDefaultsToCleanupAndFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, expression string
		want, wantError  bool
	}{
		{"default", "", true, false},
		{"preserve", "false", false, false},
		{"source-approved", "source.spec.allowed", true, false},
		{"invalid-type", "'yes'", false, true},
		{"invalid-field", "source.spec.missing", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ShouldPrune(&Spec{PruneWhen: tc.expression}, map[string]any{
				"spec": map[string]any{"allowed": true},
			})
			if got != tc.want || (err != nil) != tc.wantError {
				t.Fatalf("cleanup policy = %v, %v; want %v, error=%v", got, err, tc.want, tc.wantError)
			}
		})
	}
}
