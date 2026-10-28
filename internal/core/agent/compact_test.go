package agent

import "testing"

func TestShouldCompact(t *testing.T) {
	cases := []struct {
		name         string
		used, window int
		threshold    float64
		want         bool
	}{
		{"well under", 1000, 100000, 0.8, false},
		{"just under", 79000, 100000, 0.8, false},
		{"exactly at", 80000, 100000, 0.8, true},
		{"over", 95000, 100000, 0.8, true},
		// Disabled and unknowable cases must never fire.
		{"threshold off", 99000, 100000, 0, false},
		{"threshold negative", 99000, 100000, -1, false},
		{"window unknown", 99000, 0, 0.8, false},
		{"no usage yet", 0, 100000, 0.8, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ShouldCompact(c.used, c.window, c.threshold); got != c.want {
				t.Errorf("ShouldCompact(%d, %d, %v) = %v, want %v",
					c.used, c.window, c.threshold, got, c.want)
			}
		})
	}
}

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens(400); got != 100 {
		t.Errorf("EstimateTokens(400) = %d, want 100", got)
	}
	if got := EstimateTokens(0); got != 0 {
		t.Errorf("EstimateTokens(0) = %d, want 0", got)
	}
}
