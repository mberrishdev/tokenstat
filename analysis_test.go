package main

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestWeekForecast(t *testing.T) {
	reset := time.Date(2026, 10, 15, 9, 0, 0, 0, time.Local)
	start := reset.Add(-7 * 24 * time.Hour)
	at := func(days float64, pct float64) Usage {
		return Usage{Time: start.Add(time.Duration(days * 24 * float64(time.Hour))), Limit: &Limit{Week: pct, WeekReset: reset}}
	}
	if got := weekForecast(at(2, 50), start.Add(48*time.Hour)); !strings.Contains(got, "runs out Mon 12 Oct 09:00, 3d0h before the reset") {
		t.Errorf("fast pace: %q", got)
	}
	if got := weekForecast(at(3.5, 10), start.Add(84*time.Hour)); got != "At this pace the week ends at 20% of the limit." {
		t.Errorf("slow pace: %q", got)
	}
	if got := weekForecast(at(3.5, 10), reset.Add(time.Hour)); !strings.HasPrefix(got, "That week has ended.") {
		t.Errorf("ended week: %q", got)
	}
}

func TestLimitWeeksAndProjects(t *testing.T) {
	reset := time.Date(2026, 10, 15, 9, 0, 0, 0, time.Local)
	lim := &Limit{Plan: "pro", WeekReset: reset, WeekResetBucket: reset.Unix() / 3600}
	usage := []Usage{
		{Time: reset.Add(-48 * time.Hour), Project: "a", Limit: lim, Points: 3, Tokens: Tokens{CacheRead: 30_000_000}},
		{Time: reset.Add(-24 * time.Hour), Project: "b", Limit: lim, Points: 1, Tokens: Tokens{CacheRead: 10_000_000}},
		{Time: reset.Add(-24 * time.Hour), Project: "c", Tokens: Tokens{CacheRead: 99}},
	}
	weeks := limitWeeks(usage)
	if len(weeks) != 1 || weeks[0].points != 4 || weeks[0].tokens != 10_000_000 || weeks[0].plan != "pro" || !weeks[0].start.Equal(reset.Add(-7*24*time.Hour)) {
		t.Errorf("weeks: %+v", weeks)
	}
	rows := limitByProject(usage)
	if len(rows) != 2 || rows[0].name != "a" || rows[0].points != 3 || rows[1].name != "b" {
		t.Errorf("projects: %+v", rows)
	}
}

func TestVersionGroup(t *testing.T) {
	for _, c := range []struct {
		tool     Tool
		in, want string
	}{{Claude, "2.1.287", "2.1.28x"}, {Claude, "2.1.5", "2.1.5"}, {Codex, "0.155.1", "0.15x"}, {Codex, "0.9.1", "0.9.1"}, {Claude, "1.0", "1.0"}} {
		if got := versionGroup(c.tool, c.in); got != c.want {
			t.Errorf("versionGroup(%v, %q) = %q, want %q", c.tool, c.in, got, c.want)
		}
	}
}

func TestDrift(t *testing.T) {
	d := time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local)
	first := func(v string, ctx int64, at int) Usage {
		return Usage{Tool: Claude, Version: v, First: true, Context: ctx, Time: d.AddDate(0, 0, at),
			Tokens: Tokens{CacheWrite: ctx}, Cache1h: ctx}
	}
	usage := []Usage{
		first("2.1.260", 30000, 0), first("2.1.261", 32000, 1),
		first("2.1.280", 34000, 10), first("2.1.281", 36000, 11),
		{Tool: Claude, Version: "2.1.281", Sub: true, Time: d.AddDate(0, 0, 11), Tokens: Tokens{CacheWrite: 5000}},
	}
	rows := drift(usage)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}
	if rows[0].version != "2.1.26x" || rows[0].sessions != 2 || rows[0].start != 32000 || !math.IsNaN(rows[0].change) {
		t.Errorf("first row: %+v", rows[0])
	}
	if rows[1].start != 36000 || math.Abs(rows[1].change-0.125) > 1e-9 || rows[1].share1h != 1 || rows[1].sub1h != 0 {
		t.Errorf("second row: %+v", rows[1])
	}
}

func TestTimelineEvents(t *testing.T) {
	// The fourth call comes after a compaction. The fifth call sends less
	// context than the call before it, but only because a model filter
	// removed the call between them.
	d := time.Date(2026, 10, 1, 10, 0, 0, 0, time.Local)
	usage := []Usage{
		{Session: "s", Time: d, Context: 10_000},
		{Session: "s", Time: d.Add(time.Minute), Context: 60_000},
		{Session: "s", Time: d.Add(3 * time.Hour), Context: 61_000, Miss: 60_000, Gap: 2*time.Hour + 59*time.Minute},
		{Session: "s", Time: d.Add(3*time.Hour + time.Minute), Context: 8_000, Compact: true},
		{Session: "s", Time: d.Add(3*time.Hour + 2*time.Minute), Context: 4_000},
		{Session: "other", Time: d, Context: 1},
	}
	outputs := []Output{
		{Session: "s", Time: d.Add(time.Minute), Name: "Read", Detail: "big.log", Size: 50_000, Reads: 1},
		{Session: "s", Time: d.Add(3*time.Hour + time.Minute), Name: "Agent", Detail: "explore", Size: 2_000},
		{Session: "s", Time: d.Add(3 * time.Hour), Name: "Bash", Detail: "ls", Size: 100},
	}
	calls, events := timeline("s", usage, outputs)
	if len(calls) != 5 {
		t.Fatalf("got %d calls, want 5", len(calls))
	}
	var marks string
	for _, e := range events {
		marks += string(e.mark)
	}
	if marks != "●◆▼◇" && marks != "●◆◇▼" {
		t.Errorf("event marks %q, want output, rebuild, compaction, and subagent", marks)
	}
	if !strings.Contains(events[1].text, "after 2h59m") {
		t.Errorf("rebuild text: %q", events[1].text)
	}
}

func TestExplainDay(t *testing.T) {
	d := time.Date(2026, 10, 2, 0, 0, 0, 0, time.Local)
	usage := []Usage{
		{Time: d.Add(-24 * time.Hour), Session: "s0", Model: "m", Context: 100, Tokens: Tokens{CacheRead: 100}},
		{Time: d.Add(-48 * time.Hour), Session: "s0", Model: "m", Context: 100, Tokens: Tokens{CacheRead: 100}},
		{Time: d.Add(time.Hour), Session: "s1", Model: "m", Context: 900, Tokens: Tokens{CacheRead: 300}},
		{Time: d.Add(2 * time.Hour), Session: "s1", Model: "m", Context: 900, Miss: 6000, Gap: 2 * time.Hour, Sub: true, Tokens: Tokens{CacheRead: 300}},
	}
	outputs := []Output{{Time: d.Add(time.Hour), Name: "Bash", Detail: "make", Size: 10, Reads: 5}}
	days := dayTotals(usage, d.AddDate(0, 0, -2), d)
	text := ""
	for _, kv := range explainDay(d, days, usage, outputs, map[string]string{"s1": "Fix the build"}) {
		text += kv[0] + ": " + kv[1] + "\n"
	}
	for _, want := range []string{"600 tokens, 6.0× the median day (100)", "100%  Fix the build",
		"1 rebuilds after more than 1 hour idle", "Subagents: 50%", "Bash make added 10; 5 later calls", "Models: m 100%"} {
		if !strings.Contains(text, want) {
			t.Errorf("explanation lacks %q:\n%s", want, text)
		}
	}
}

func TestClip(t *testing.T) {
	if got := clip("~/Code/editor/Sources/Editor/HotKey.swift", 21); got != "…/Editor/HotKey.swift" {
		t.Errorf("path: %q", got)
	}
	if got := clip("swift build 2>&1 | grep error", 12); got != "swift build…" {
		t.Errorf("command: %q", got)
	}
}
