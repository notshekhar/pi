package trace

import "testing"

func TestJSFormatting(t *testing.T) {
	cases := map[string]string{
		toFixed(2.25, 1): "2.3", toFixed(1.005, 2): "1.00", toFixed(9.96, 1): "10.0", toFixed(0.5, 0): "1",
		toFixed(12.5, 0): "13", toPrecision2(0.0034): "0.0034", toPrecision2(0.00999): "0.010",
		FmtMs(65_400): "1 min 5 s", FmtMs(999.5): "1000 ms", FmtMs(12_340): "12 s",
		jsNumber(1e21): "1e+21", jsNumber(0.0000001): "1e-7", commas(1234567): "1,234,567",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
	if l := toolInputLine([]byte(`{"path":"/x","command":"ls  -la\n","n":2}`), 200); l != "ls -la · path: /x · n: 2" {
		t.Errorf("input line %q", l)
	}
}
