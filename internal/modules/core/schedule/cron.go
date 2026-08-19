// Package schedule parses cron expressions and answers "when does this fire
// next".
//
// Written rather than imported because this module has no external
// dependencies, and cron is small enough to own: five or six fields, each a
// set of integers, and a next-fire search that walks minutes forward. What
// makes a cron implementation wrong is almost never the parser — it is the
// search, and specifically day-of-month versus day-of-week, where the
// standard says the two are OR'd when both are restricted and AND'd when only
// one is. That rule is the reason this is a file rather than three lines of
// modulo arithmetic.
package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed expression.
type Cron struct {
	seconds  fieldSet
	minutes  fieldSet
	hours    fieldSet
	days     fieldSet
	months   fieldSet
	weekdays fieldSet
	// daysRestricted and weekdaysRestricted record whether the field was
	// written as `*`. See Next for why it matters.
	daysRestricted     bool
	weekdaysRestricted bool
	expr               string
}

// fieldSet is the set of allowed values for one field, indexed from the
// field's own minimum.
type fieldSet []bool

func (f fieldSet) has(v int, min int) bool {
	i := v - min
	return i >= 0 && i < len(f) && f[i]
}

// bounds describe one field's legal range.
type bounds struct {
	name     string
	min, max int
	// names maps three-letter aliases (jan, mon) onto values.
	names map[string]int
}

var (
	secondBounds = bounds{"second", 0, 59, nil}
	minuteBounds = bounds{"minute", 0, 59, nil}
	hourBounds   = bounds{"hour", 0, 23, nil}
	domBounds    = bounds{"day of month", 1, 31, nil}
	monthBounds  = bounds{"month", 1, 12, map[string]int{
		"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
		"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
	}}
	weekdayBounds = bounds{"day of week", 0, 6, map[string]int{
		"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	}}
)

// Parse reads a 5- or 6-field expression.
//
// Six fields put SECONDS first, matching the second-level spelling loop
// accepts (`0 */45 * * * *`); five fields start at minutes, which is what
// crontab means. Anything else is rejected rather than guessed at — a
// misread field silently fires at the wrong time forever.
func Parse(expr string) (*Cron, error) {
	fields := strings.Fields(strings.TrimSpace(expr))
	switch len(fields) {
	case 5:
		fields = append([]string{"0"}, fields...)
	case 6:
	default:
		return nil, fmt.Errorf("cron needs 5 or 6 fields, got %d", len(fields))
	}

	c := &Cron{expr: strings.TrimSpace(expr)}
	var err error
	if c.seconds, err = parseField(fields[0], secondBounds); err != nil {
		return nil, err
	}
	if c.minutes, err = parseField(fields[1], minuteBounds); err != nil {
		return nil, err
	}
	if c.hours, err = parseField(fields[2], hourBounds); err != nil {
		return nil, err
	}
	if c.days, err = parseField(fields[3], domBounds); err != nil {
		return nil, err
	}
	if c.months, err = parseField(fields[4], monthBounds); err != nil {
		return nil, err
	}
	if c.weekdays, err = parseField(fields[5], weekdayBounds); err != nil {
		return nil, err
	}
	c.daysRestricted = !isWildcard(fields[3])
	c.weekdaysRestricted = !isWildcard(fields[5])
	return c, nil
}

// String is the expression as written.
func (c *Cron) String() string { return c.expr }

func isWildcard(field string) bool { return field == "*" || field == "?" }

// parseField reads one comma-separated field: `*`, `a`, `a-b`, and any of
// those with a `/step`.
func parseField(field string, b bounds) (fieldSet, error) {
	set := make(fieldSet, b.max-b.min+1)
	for _, part := range strings.Split(field, ",") {
		if err := parsePart(part, b, set); err != nil {
			return nil, err
		}
	}
	return set, nil
}

func parsePart(part string, b bounds, set fieldSet) error {
	part = strings.TrimSpace(part)
	if part == "" {
		return fmt.Errorf("empty %s field", b.name)
	}

	step := 1
	if base, stepText, found := strings.Cut(part, "/"); found {
		n, err := strconv.Atoi(stepText)
		if err != nil || n <= 0 {
			return fmt.Errorf("bad step %q in %s", stepText, b.name)
		}
		step, part = n, base
		// `*/5` and `5/5` differ: the first is the whole range stepped, the
		// second starts at 5 and runs to the maximum.
		if part == "" {
			part = "*"
		}
	}

	var from, to int
	switch {
	case isWildcard(part):
		from, to = b.min, b.max
	default:
		lo, hi, isRange := strings.Cut(part, "-")
		var err error
		if from, err = parseValue(lo, b); err != nil {
			return err
		}
		if isRange {
			if to, err = parseValue(hi, b); err != nil {
				return err
			}
		} else if step > 1 {
			// A bare value with a step runs to the end of the range.
			to = b.max
		} else {
			to = from
		}
	}
	if from > to {
		return fmt.Errorf("%s range %d-%d is backwards", b.name, from, to)
	}
	for v := from; v <= to; v += step {
		set[v-b.min] = true
	}
	return nil
}

func parseValue(text string, b bounds) (int, error) {
	text = strings.TrimSpace(text)
	if b.names != nil {
		if v, ok := b.names[strings.ToLower(text)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("bad %s value %q", b.name, text)
	}
	// Sunday is both 0 and 7 in the wild, and a schedule written the other
	// way must not silently never fire.
	if b.name == weekdayBounds.name && v == 7 {
		v = 0
	}
	if v < b.min || v > b.max {
		return 0, fmt.Errorf("%s %d out of range %d-%d", b.name, v, b.min, b.max)
	}
	return v, nil
}

// maxSearchYears bounds the forward search. An expression like `0 0 0 30 2 *`
// — the thirtieth of February — matches nothing, and without a bound the
// search for its next firing never returns.
const maxSearchYears = 5

// Next is the first firing strictly after `after`, or the zero time when the
// expression can never match.
func (c *Cron) Next(after time.Time) time.Time {
	// Second resolution, starting at the next whole second so a schedule
	// cannot fire twice for the same instant.
	at := after.Truncate(time.Second).Add(time.Second)
	limit := after.AddDate(maxSearchYears, 0, 0)

	for at.Before(limit) {
		if !c.months.has(int(at.Month()), monthBounds.min) {
			// Jump to the first instant of the next month rather than
			// stepping a second at a time through one that cannot match.
			at = time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, at.Location()).AddDate(0, 1, 0)
			continue
		}
		if !c.matchesDay(at) {
			at = time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, at.Location()).AddDate(0, 0, 1)
			continue
		}
		if !c.hours.has(at.Hour(), hourBounds.min) {
			at = time.Date(at.Year(), at.Month(), at.Day(), at.Hour(), 0, 0, 0, at.Location()).Add(time.Hour)
			continue
		}
		if !c.minutes.has(at.Minute(), minuteBounds.min) {
			at = at.Truncate(time.Minute).Add(time.Minute)
			continue
		}
		if !c.seconds.has(at.Second(), secondBounds.min) {
			at = at.Add(time.Second)
			continue
		}
		return at
	}
	return time.Time{}
}

// matchesDay applies the standard's day rule: when BOTH day-of-month and
// day-of-week are restricted the schedule fires if EITHER matches; when only
// one is restricted, only that one is consulted.
//
// This is the part of cron everyone gets wrong. `0 0 1 * 1` is the first of
// the month and every Monday, not the first of the month when it is a Monday.
func (c *Cron) matchesDay(at time.Time) bool {
	dom := c.days.has(at.Day(), domBounds.min)
	dow := c.weekdays.has(int(at.Weekday()), weekdayBounds.min)
	switch {
	case c.daysRestricted && c.weekdaysRestricted:
		return dom || dow
	case c.daysRestricted:
		return dom
	case c.weekdaysRestricted:
		return dow
	}
	return true
}
