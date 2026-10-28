package agent

import (
	"math"
	"sort"
	"time"
)

// buildSteakGrid (agent/steak.ts): the /steak token heatmap, computed once
// so the terminal and every client shade identically.

var steakMonths = []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

// SteakStats is loop's SteakStats.
type SteakStats struct {
	CurrentStreak    int    `json:"currentStreak"`
	LongestStreak    int    `json:"longestStreak"`
	ActiveDays       int    `json:"activeDays"`
	BusiestDay       string `json:"busiestDay"`
	BusiestDayTokens int64  `json:"busiestDayTokens"`
}

// SteakGrid is loop's SteakGrid.
type SteakGrid struct {
	TotalTokens int64      `json:"totalTokens"`
	Stats       SteakStats `json:"stats"`
	Weeks       int        `json:"weeks"`
	Cells       [][]int    `json:"cells"`
	Tokens      [][]int64  `json:"tokens"`
	StartDay    string     `json:"startDay"`
	MonthLabels []string   `json:"monthLabels"`
	Thresholds  [3]int64   `json:"thresholds"`
}

func steakDay(t time.Time) string { return t.Format("2006-01-02") }

func localDate(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}

// BuildSteakGrid builds the grid from a day → tokens map. year 0 is the
// trailing 52 weeks.
func BuildSteakGrid(daily map[string]int64, year int, now time.Time) SteakGrid {
	var firstSunday, lastDay, lower time.Time
	if year != 0 {
		jan1 := localDate(year, time.January, 1)
		lower, lastDay = jan1, localDate(year, time.December, 31)
		firstSunday = localDate(year, time.January, 1-int(jan1.Weekday()))
	} else {
		today := localDate(now.Year(), now.Month(), now.Day())
		lastDay = today
		lastSunday := localDate(today.Year(), today.Month(), today.Day()-int(today.Weekday()))
		firstSunday = localDate(lastSunday.Year(), lastSunday.Month(), lastSunday.Day()-52*7)
		lower = firstSunday
	}
	weeks := int(math.Round(float64(lastDay.Sub(firstSunday))/float64(7*24*time.Hour))) + 1
	today := localDate(now.Year(), now.Month(), now.Day())
	isYear := year != 0

	raw := make([][]int64, 7)
	for r := range raw {
		raw[r] = make([]int64, weeks)
		for c := range raw[r] {
			raw[r][c] = -1
		}
	}
	var nonZero []int64
	var total int64
	type day struct {
		key string
		tok int64
	}
	var counted []day
	for c := 0; c < weeks; c++ {
		for r := 0; r < 7; r++ {
			d := localDate(firstSunday.Year(), firstSunday.Month(), firstSunday.Day()+c*7+r)
			future := d.After(today)
			if future && !isYear {
				continue
			}
			in := !future && !d.Before(lower) && !d.After(lastDay)
			var tok int64
			if in {
				tok = daily[steakDay(d)]
			}
			raw[r][c] = tok
			if in {
				total += tok
				counted = append(counted, day{steakDay(d), tok})
			}
			if tok > 0 {
				nonZero = append(nonZero, tok)
			}
		}
	}
	var stats SteakStats
	run := 0
	var busiest *day
	for i := range counted {
		d := counted[i]
		if d.tok > 0 {
			run++
			stats.ActiveDays++
			stats.LongestStreak = max(stats.LongestStreak, run)
			if busiest == nil || d.tok > busiest.tok {
				busiest = &counted[i]
			}
		} else {
			run = 0
		}
	}
	i := len(counted) - 1
	if i >= 0 && counted[i].tok == 0 {
		i--
	}
	for ; i >= 0 && counted[i].tok > 0; i-- {
		stats.CurrentStreak++
	}
	if busiest != nil {
		stats.BusiestDay, stats.BusiestDayTokens = busiest.key, busiest.tok
	}
	sort.Slice(nonZero, func(a, b int) bool { return nonZero[a] < nonZero[b] })
	q := func(p float64) int64 {
		if len(nonZero) == 0 {
			return 0
		}
		return nonZero[int(math.Floor(p*float64(len(nonZero)-1)))]
	}
	q1, q2, q3 := q(0.25), q(0.5), q(0.75)
	level := func(t int64) int {
		switch {
		case t < 0:
			return -1
		case t == 0:
			return 0
		case t <= q1:
			return 1
		case t <= q2:
			return 2
		case t <= q3:
			return 3
		}
		return 4
	}
	cells := make([][]int, 7)
	for r := range raw {
		cells[r] = make([]int, weeks)
		for c, t := range raw[r] {
			cells[r][c] = level(t)
		}
	}
	monthOf := func(c int) time.Month {
		return localDate(firstSunday.Year(), firstSunday.Month(), firstSunday.Day()+c*7).Month()
	}
	labels := make([]string, weeks)
	prev := time.Month(0)
	for c := 0; c < weeks; c++ {
		if m := monthOf(c); m != prev {
			labels[c] = steakMonths[m-1]
			prev = m
		}
	}
	if weeks > 1 && labels[0] != "" && monthOf(0) != monthOf(1) {
		labels[0] = ""
	}
	if weeks > 1 && labels[weeks-1] != "" && monthOf(weeks-1) != monthOf(weeks-2) {
		labels[weeks-1] = ""
	}
	return SteakGrid{TotalTokens: total, Stats: stats, Weeks: weeks, Cells: cells, Tokens: raw,
		StartDay: steakDay(firstSunday), MonthLabels: labels, Thresholds: [3]int64{q1, q2, q3}}
}
