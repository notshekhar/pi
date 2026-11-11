package main

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/core/schedule"
)

// `pi goals …` — loop's goals/commands.ts: the CLI face of /background.
//
// `goals daemon` is not ported: loop's daemon (a launchd agent or systemd
// timer running `loop goals tick`) already ticks this same table, and a
// second scheduler would only race it.

// localeTime is JavaScript's en-US toLocaleString, which loop's listing uses.
const localeTime = "1/2/2006, 3:04:05 PM"

// goalIDShown is how much of a ULID the listing prints — enough to be a
// unique prefix for `rm` and `run` at twenty goals.
const goalIDShown = 8

func goalsCommand(c *cliCall) int {
	sub, rest := "", []string(nil)
	if p := c.args.positional; len(p) > 0 {
		sub, rest = p[0], p[1:]
	}
	switch sub {
	case "list":
		return goalsList()
	case "add":
		return goalsAdd(c, rest)
	case "rm":
		return goalsRemove(rest)
	case "run":
		return goalsRun(c, rest)
	case "tick":
		return goalsTick(c)
	case "daemon":
		fmt.Fprintf(os.Stderr, "%s goals daemon is not available — loop's scheduler ticks the same goals: loop goals daemon install\n", productName)
		return 1
	}
	printGoalsUsage()
	if sub != "" && sub != "help" {
		return 1
	}
	return 0
}

func printGoalsUsage() {
	fmt.Printf(`Usage: %s goals <command>

  list                          show background tasks with schedule and last-run status
  add <text> [options]          add a background task
      --cron "<expr>"           run on a cron schedule
      --every <30m|2h|1d>       run on an interval (sugar for --cron)
      --at "<when>"             run once (e.g. "18:30", "2h", "tomorrow 9:00")
      --cwd <dir>               directory the task runs in (default: here)
      --model <provider/model>  model for scheduled runs
      --agent <name>            agent the task runs under
  rm <id-prefix>                delete a background task
  run <id-prefix>               run a background task now (headless)
  tick                          run all due tasks, then exit (what the daemon calls)

Goals without a schedule run on demand: fire one with `+"`goals run <id>`"+`
(the TUI's /goal quick-add runs them immediately).
`, productName)
}

func describeSchedule(g goal) string {
	switch g.kind {
	case "cron":
		return "cron " + g.expr
	case "once":
		return "once " + g.at.Format(localeTime)
	}
	return "on demand"
}

func describeLastRun(g goal) string {
	if g.lastRun == nil {
		return "never ran"
	}
	return g.lastRun.status + " " + g.lastRun.at.Format(localeTime)
}

func shortGoalID(id string) string { return id[:min(goalIDShown, len(id))] }

func goalsList() int {
	goals, err := listGoals()
	if err != nil {
		return goalError(err)
	}
	if len(goals) == 0 {
		fmt.Printf("no goals — add one with: %s goals add \"<text>\"\n", productName)
		return 0
	}
	for _, g := range goals {
		state := ""
		if !g.enabled {
			state = " (disabled)"
		}
		fmt.Printf("%s  %s%s  %s\n", shortGoalID(g.id), describeSchedule(g), state, describeLastRun(g))
		fmt.Printf("          %s  [%s]\n", g.text, g.cwd)
	}
	return 0
}

func goalsAdd(c *cliCall, rest []string) int {
	text := strings.TrimSpace(strings.Join(rest, " "))
	if text == "" {
		fmt.Fprintf(os.Stderr, "usage: %s goals add \"<text>\" [--cron|--every|--at] [--cwd] [--model]\n", productName)
		return 1
	}
	sched, msg := goalScheduleFromFlags(c.args)
	if msg != "" {
		fmt.Fprintln(os.Stderr, msg)
		return 1
	}
	model, _ := c.args.flag("model")
	agent, _ := c.args.flag("agent")
	g, err := addGoal(text, c.cwd(), sched, model, agent)
	if err != nil {
		return goalError(err)
	}
	fmt.Printf("added goal %s (%s)\n", shortGoalID(g.id), describeSchedule(g))
	return 0
}

// goalScheduleFromFlags reads --every, --cron or --at (first wins, in that
// order), or says why it cannot.
func goalScheduleFromFlags(a cliArgs) (goalSchedule, string) {
	if every, ok := a.flag("every"); ok {
		expr := everyToCron(every)
		if expr == "" {
			return goalSchedule{}, fmt.Sprintf("cannot express --every %s as a schedule (try 30m, 2h, 1d)", every)
		}
		return goalSchedule{kind: "cron", expr: expr}, ""
	}
	if expr, ok := a.flag("cron"); ok {
		if _, err := schedule.Parse(expr); err != nil {
			return goalSchedule{}, "invalid cron expression: " + expr
		}
		return goalSchedule{kind: "cron", expr: expr}, ""
	}
	if at, ok := a.flag("at"); ok {
		when, ok := parseOnceWhen(at, time.Now())
		if !ok {
			return goalSchedule{}, fmt.Sprintf(`cannot parse --at %s (try "18:30", "45m", "tomorrow 9:00")`, at)
		}
		return goalSchedule{kind: "once", at: when}, ""
	}
	return goalSchedule{kind: "none"}, ""
}

var everyPattern = regexp.MustCompile(`(?i)^(\d+)\s*(m|min|h|hr|d|day)?s?$`)

// Units for --every's cron sugar.
const (
	minutesPerHour = 60
	hoursPerDay    = 24
)

// everyToCron turns "30m" / "2h" / "1d" / plain minutes into a cron
// expression anchored at zero, or "" for what has no clean equivalent (90m).
func everyToCron(spec string) string {
	m := everyPattern.FindStringSubmatch(strings.TrimSpace(spec))
	if m == nil {
		return ""
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return ""
	}
	switch unit := strings.ToLower(m[2]); {
	case unit == "" || strings.HasPrefix(unit, "m"):
		switch {
		case n < minutesPerHour:
			return fmt.Sprintf("*/%d * * * *", n)
		case n%minutesPerHour == 0:
			return fmt.Sprintf("0 */%d * * *", n/minutesPerHour)
		}
		return ""
	case strings.HasPrefix(unit, "h"):
		switch {
		case n < hoursPerDay:
			return fmt.Sprintf("0 */%d * * *", n)
		case n%hoursPerDay == 0:
			return fmt.Sprintf("0 0 */%d * *", n/hoursPerDay)
		}
		return ""
	}
	return fmt.Sprintf("0 0 */%d * *", n)
}

// goalByPrefix finds one goal or prints why not.
func goalByPrefix(prefix string) (goal, bool) {
	g, err := findGoalByPrefix(prefix)
	if err == nil {
		return g, true
	}
	if isAmbiguous(err) {
		fmt.Fprintln(os.Stderr, err)
	}
	fmt.Fprintf(os.Stderr, "no goal matching %q\n", prefix)
	return goal{}, false
}

func goalsRemove(rest []string) int {
	if len(rest) == 0 {
		fmt.Fprintf(os.Stderr, "usage: %s goals rm <id-prefix>\n", productName)
		return 1
	}
	g, ok := goalByPrefix(rest[0])
	if !ok {
		return 1
	}
	if err := deleteGoal(g.id); err != nil {
		return goalError(err)
	}
	fmt.Printf("deleted: %s\n", g.text)
	return 0
}

func goalsRun(c *cliCall, rest []string) int {
	if len(rest) == 0 {
		fmt.Fprintf(os.Stderr, "usage: %s goals run <id-prefix>\n", productName)
		return 1
	}
	g, ok := goalByPrefix(rest[0])
	if !ok {
		return 1
	}
	result := runGoal(c.ctx, g)
	fmt.Printf("%s: %s\n", result.status, result.summary)
	if result.status == "error" {
		return 1
	}
	return 0
}

func goalError(err error) int {
	fmt.Fprintln(os.Stderr, "error:", err)
	return 1
}
