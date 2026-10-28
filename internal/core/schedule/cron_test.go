package schedule

import (
	"testing"
	"time"
)

func at(text string) time.Time {
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", text, time.Local)
	if err != nil {
		panic(err)
	}
	return parsed
}

func TestNext(t *testing.T) {
	cases := []struct {
		expr, from, want string
	}{
		// Five fields start at minutes.
		{"* * * * *", "2026-08-18 10:00:00", "2026-08-18 10:01:00"},
		{"30 9 * * *", "2026-08-18 10:00:00", "2026-08-19 09:30:00"},
		{"30 9 * * *", "2026-08-18 08:00:00", "2026-08-18 09:30:00"},
		// Six fields put seconds first.
		{"0 */45 * * * *", "2026-08-18 10:00:01", "2026-08-18 10:45:00"},
		{"*/15 * * * * *", "2026-08-18 10:00:01", "2026-08-18 10:00:15"},
		// Ranges, lists, and names.
		{"0 0 9-17 * * *", "2026-08-18 20:00:00", "2026-08-19 09:00:00"},
		{"0 0 0 1 jan *", "2026-08-18 10:00:00", "2027-01-01 00:00:00"},
		{"0 0 12 * * mon", "2026-08-18 10:00:00", "2026-08-24 12:00:00"},
		{"0 0,30 * * * *", "2026-08-18 10:05:00", "2026-08-18 10:30:00"},
		// Sunday spelled 7 is the same as 0.
		{"0 0 8 * * 7", "2026-08-18 10:00:00", "2026-08-23 08:00:00"},
	}
	for _, c := range cases {
		cron, err := Parse(c.expr)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.expr, err)
			continue
		}
		if got := cron.Next(at(c.from)); !got.Equal(at(c.want)) {
			t.Errorf("Parse(%q).Next(%s) = %s, want %s", c.expr, c.from, got.Format(time.DateTime), c.want)
		}
	}
}

// The standard's day rule: with both day fields restricted, EITHER matching
// fires. Getting this backwards is the classic cron bug — `0 0 1 * 1` would
// then only fire on a first-of-the-month that happened to be a Monday.
func TestDayOfMonthOrWeekday(t *testing.T) {
	cron, err := Parse("0 0 1 * 1")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-08-18 is a Tuesday; the next Monday is the 24th, well before the
	// 1st of September.
	if got, want := cron.Next(at("2026-08-18 10:00:00")), at("2026-08-24 00:00:00"); !got.Equal(want) {
		t.Errorf("weekday branch: got %s, want %s", got.Format(time.DateTime), want.Format(time.DateTime))
	}
	// With only day-of-month restricted, weekdays are not consulted at all.
	monthly, err := Parse("0 0 1 * *")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := monthly.Next(at("2026-08-18 10:00:00")), at("2026-09-01 00:00:00"); !got.Equal(want) {
		t.Errorf("day-of-month only: got %s, want %s", got.Format(time.DateTime), want.Format(time.DateTime))
	}
}

// An expression that can never match must return, not spin.
func TestImpossibleExpressionTerminates(t *testing.T) {
	cron, err := Parse("0 0 0 30 2 *")
	if err != nil {
		t.Fatal(err)
	}
	if got := cron.Next(at("2026-08-18 10:00:00")); !got.IsZero() {
		t.Errorf("Feb 30 fired at %s", got)
	}
}

func TestParseRejectsJunk(t *testing.T) {
	for _, expr := range []string{"", "* * *", "* * * * * * *", "60 * * * *", "* * * * xyz", "*/0 * * * *"} {
		if _, err := Parse(expr); err == nil {
			t.Errorf("Parse(%q) accepted", expr)
		}
	}
}

// Next is strictly after its argument, so a schedule cannot fire twice for
// the same instant.
func TestNextIsStrictlyAfter(t *testing.T) {
	cron, err := Parse("0 0 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	noon := at("2026-08-18 12:00:00")
	if got := cron.Next(noon); !got.After(noon) {
		t.Errorf("Next(%s) = %s, not strictly after", noon, got)
	}
}
