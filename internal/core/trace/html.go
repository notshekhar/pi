package trace

import (
	_ "embed"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
)

var (
	//go:embed data/styles.css
	css string
	//go:embed data/markdown.js
	mdJS string
	//go:embed data/client.js
	clientJS string
)

// FmtMs is loop's fmtMs.
func FmtMs(ms float64) string {
	if ms < 1000 {
		return jsNumber(math.Round(ms)) + " ms"
	}
	if ms < 60_000 {
		d := 0
		if ms < 10_000 {
			d = 1
		}
		return toFixed(ms/1000, d) + " s"
	}
	m := math.Floor(ms / 60_000)
	s := math.Round(math.Mod(ms, 60_000) / 1000)
	if s != 0 {
		return fmt.Sprintf("%s min %s s", jsNumber(m), jsNumber(s))
	}
	return jsNumber(m) + " min"
}

// toFixed is Number#toFixed: of the two nearest d-digit decimals, the one
// closer to the double's EXACT value, the larger on a tie — not Go's
// round-half-even (2.25 → "2.3" in JS, "2.2" from strconv).
func toFixed(f float64, d int) string {
	neg := f < 0
	exact := new(big.Float).SetPrec(2048).SetFloat64(math.Abs(f)).Text('f', 1100)
	intPart, frac, _ := strings.Cut(exact, ".")
	frac += strings.Repeat("0", d+1)
	digits := []byte(intPart + frac[:d])
	if frac[d] >= '5' {
		i := len(digits) - 1
		for ; i >= 0; i-- {
			if digits[i] == '9' {
				digits[i] = '0'
				continue
			}
			digits[i]++
			break
		}
		if i < 0 {
			digits = append([]byte{'1'}, digits...)
		}
	}
	whole := string(digits[:len(digits)-d])
	out := whole
	if d > 0 {
		out += "." + string(digits[len(digits)-d:])
	}
	if neg && strings.Trim(out, "0.") != "" {
		out = "-" + out
	}
	return out
}

// FmtUSD is loop's fmtUsd.
func FmtUSD(usd *float64) string {
	if usd == nil {
		return "—"
	}
	u := *usd
	switch {
	case u == 0:
		return "$0"
	case u < 0.000001:
		return "<$0.000001"
	case u < 0.01:
		return "$" + toPrecision2(u)
	case u < 1:
		return "$" + toFixed(u, 3)
	}
	return "$" + toFixed(u, 2)
}

// toPrecision2 is Number#toPrecision(2) for 1e-6 <= u < 0.01 (fixed form).
func toPrecision2(u float64) string {
	e := int(math.Floor(math.Log10(u)))
	digits := 1 - e
	s := toFixed(u, digits)
	// Rounding up can carry into a new leading digit (0.0099 → 0.010).
	if f, _ := strconv.ParseFloat(s, 64); f > 0 && int(math.Floor(math.Log10(f))) > e {
		s = toFixed(u, digits-1)
	}
	return s
}

func plural(n int, one string) string {
	word := one + "s"
	if n == 1 {
		word = one
	}
	return commas(int64(n)) + " " + word
}

var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

func esc(s string) string { return htmlEscaper.Replace(s) }

// Headline is loop's traceHeadline.
func Headline(m Model) string {
	t := m.Totals
	head := fmt.Sprintf("%s, %s, %s", plural(t.Turns, "turn"), plural(t.Steps, "step"), plural(t.Tools, "tool call"))
	est := ""
	if t.Usage.Estimated {
		est = " (part estimated)"
	}
	return fmt.Sprintf("%s — %s of wall clock, %s%s.", head, FmtMs(t.WallMs), FmtUSD(t.Usage.USD), est)
}

// CoverageLine is loop's coverageLine.
func CoverageLine(c Coverage) string {
	steps := c.Recorded + c.Derived + c.None
	if steps == 0 {
		return "no steps yet"
	}
	if c.Recorded == steps {
		return "timing recorded for every step"
	}
	return fmt.Sprintf("timing recorded for %d of %d steps · %d wall-time only · %d not recorded", c.Recorded, steps, c.Derived, c.None)
}

// localeString is en-US Date#toLocaleString.
func localeString(ms int64) string {
	return time.UnixMilli(ms).Format("1/2/2006, 3:04:05 PM")
}

func embedJSON(m Model) string {
	raw, _ := marshal(m)
	// A literal "<" would end the script element early; JSON.parse reads
	// the escape back unchanged.
	return strings.ReplaceAll(string(raw), "<", "\\u003c")
}

func masthead(m Model, title string) string {
	s, c := m.Session, m.Coverage
	coverage := CoverageLine(c)
	return fmt.Sprintf(`<header class="mast">
  <p class="eyebrow">loop trace <span class="sep">·</span> <span class="mono">%s</span></p>
  <h1>%s</h1>
  <p class="meta mono">%s <span class="sep">·</span> %s <span class="sep">·</span> started %s</p>
  <p class="headline">%s</p>
  <div class="coverage" role="img" aria-label="%s">
    <div class="coverage-bar"><span class="cov recorded" style="flex-grow:%d"></span><span class="cov derived" style="flex-grow:%d"></span><span class="cov none" style="flex-grow:%d"></span></div>
    <p class="coverage-line mono">%s</p>
  </div>
</header>`, esc(s.ID), esc(title), esc(s.Model), esc(s.CWD), esc(localeString(s.CreatedAt)), esc(Headline(m)),
		esc(coverage), c.Recorded, c.Derived, c.None, esc(coverage))
}

// Title is the page title: the session's name, else "session <id>".
func Title(m Model) string {
	if m.Session.Name != "" {
		return m.Session.Name
	}
	return "session " + m.Session.ID
}

// HTML is loop's renderTraceHtml.
func HTML(m Model) string {
	title := Title(m)
	return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>trace · ` + esc(title) + `</title>
<style>` + css + `</style>
</head>
<body>
` + masthead(m, title) + `
<main id="app"><noscript><p class="mono">This page draws the trace with script. The data is in the page; enable script to see it.</p></noscript></main>
<footer class="foot mono">generated ` + esc(localeString(m.GeneratedAt)) + ` <span class="sep">·</span> timings are observed by the loop process, good for a trace, not a latency benchmark</footer>
<script id="trace" type="application/json">` + embedJSON(m) + `</script>
<script>` + mdJS + `</script>
<script>` + clientJS + `</script>
</body>
</html>
`
}
