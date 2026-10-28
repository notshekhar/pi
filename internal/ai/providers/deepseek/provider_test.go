package deepseek

import "testing"

// The AI SDK's isDeepSeekV4Model cases.
func TestIsV4Model(t *testing.T) {
	for id, want := range map[string]bool{
		"deepseek-v4-flash": true, "deepseek-v4-pro": true, "deepseek-flash": true, "deepseek-v5": true,
		"deepseek-chat": false, "deepseek-reasoner": false, "deepseek-v3": false, "deepseek-v3.2": false,
		"deepseek/deepseek-v3.1-terminus": false, "deepseek-v3-0324": false,
	} {
		if got := IsV4Model(id); got != want {
			t.Errorf("IsV4Model(%q) = %v, want %v", id, got, want)
		}
	}
}
