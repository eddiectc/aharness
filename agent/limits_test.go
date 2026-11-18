package agent

import "testing"

func TestNewLimits(t *testing.T) {
	tests := []struct {
		name      string
		context   int
		output    int
		inputCap  int
		want      Limits
		wantError bool
	}{
		{
			name: "floor margin", context: 10_000, output: 1_000, inputCap: 0,
			want: Limits{MaxContextTokens: 10_000, MaxOutputTokens: 1_000, SafetyMargin: 1000, MaxInputTokens: 9000, CompactionTarget: 9000},
		},
		{
			name: "five percent margin", context: 100_000, output: 2_000, inputCap: 0,
			want: Limits{MaxContextTokens: 100_000, MaxOutputTokens: 2_000, SafetyMargin: 5000, MaxInputTokens: 95_000, CompactionTarget: 95_000},
		},
		{
			name: "input cap", context: 100_000, output: 2_000, inputCap: 50_000,
			want: Limits{MaxContextTokens: 100_000, MaxOutputTokens: 2_000, SafetyMargin: 5000, MaxInputTokens: 50_000, CompactionTarget: 50_000},
		},
		{name: "zero context", context: 0, wantError: true},
		{name: "negative output", context: 10_000, output: -1, wantError: true},
		{name: "negative input cap", context: 10_000, inputCap: -1, wantError: true},
		{name: "input cap over context", context: 10_000, inputCap: 20_000, wantError: true},
		{name: "margin consumes context", context: 500, wantError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewLimits(tc.context, tc.output, tc.inputCap)
			if tc.wantError {
				if err == nil {
					t.Fatalf("NewLimits = %+v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewLimits: %v", err)
			}
			if got != tc.want {
				t.Fatalf("NewLimits = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestLimitsMaxOutputSet(t *testing.T) {
	if (Limits{}).MaxOutputSet() {
		t.Fatal("zero output reported as set")
	}
	if !(Limits{MaxOutputTokens: 1}).MaxOutputSet() {
		t.Fatal("positive output reported as unset")
	}
}
