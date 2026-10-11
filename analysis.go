package main

// The analyses behind the Advanced views. Each one takes the usage and
// outputs that the filters kept.

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

func median(xs []int64) int64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]int64(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

// duration formats d as "1h12m", "35m", or "2d4h".
func duration(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// Limit forensics

// latestLimit returns the newest call with a Codex limit snapshot.
func latestLimit(usage []Usage) (Usage, bool) {
	var latest Usage
	for _, u := range usage {
		if u.Limit != nil && u.Time.After(latest.Time) {
			latest = u
		}
	}
	return latest, latest.Limit != nil
}

// weekForecast says where the weekly limit ends if the pace of the week so
// far goes on.
func weekForecast(u Usage, now time.Time) string {
	l := u.Limit
	if !now.Before(l.WeekReset) {
		return "That week has ended. Codex has not run since " + u.Time.Format("Mon 2 Jan 15:04") + "."
	}
	start := l.WeekReset.Add(-7 * 24 * time.Hour)
	elapsed := u.Time.Sub(start)
	if l.Week <= 0 || elapsed <= 0 {
		return "No weekly limit used yet."
	}
	empty := start.Add(time.Duration(float64(elapsed) * 100 / l.Week))
	if empty.Before(l.WeekReset) {
		return fmt.Sprintf("At this pace the limit runs out %s, %s before the reset.",
			empty.Format("Mon 2 Jan 15:04"), duration(l.WeekReset.Sub(empty)))
	}
	return fmt.Sprintf("At this pace the week ends at %.0f%% of the limit.", l.Week*float64(7*24*time.Hour)/float64(elapsed))
}

type limitRow struct {
	name   string
	points float64
	tokens int64
}

// limitByProject sums the weekly limit points that each project used, most
// first.
func limitByProject(usage []Usage) []limitRow {
	rows := map[string]*limitRow{}
	for _, u := range usage {
		if u.Limit == nil {
			continue
		}
		r := rows[u.Project]
		if r == nil {
			r = &limitRow{name: u.Project}
			rows[u.Project] = r
		}
		r.points += u.Points
		r.tokens += u.Total()
	}
	out := make([]limitRow, 0, len(rows))
	for _, r := range rows {
		if r.points > 0 {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].points > out[j].points })
	return out
}

type limitWeek struct {
	start  time.Time
	plan   string
	points float64
	tokens int64
}

// limitWeeks returns the limit points and tokens of each weekly window,
// oldest first. The tokens leave out the first call of each week, because
// that call only sets the start of the percent.
func limitWeeks(usage []Usage) []limitWeek {
	weeks := map[int64]*limitWeek{}
	latest := map[int64]time.Time{}
	earliest := map[int64]Usage{}
	for _, u := range usage {
		l := u.Limit
		if l == nil {
			continue
		}
		w := weeks[l.WeekResetBucket]
		if w == nil {
			w = &limitWeek{start: l.WeekReset.Add(-7 * 24 * time.Hour).Local()}
			weeks[l.WeekResetBucket] = w
		}
		if u.Time.After(latest[l.WeekResetBucket]) {
			w.plan, latest[l.WeekResetBucket] = l.Plan, u.Time
		}
		if e, ok := earliest[l.WeekResetBucket]; !ok || u.Time.Before(e.Time) {
			earliest[l.WeekResetBucket] = u
		}
		w.points += u.Points
		w.tokens += u.Total()
	}
	out := make([]limitWeek, 0, len(weeks))
	for b, w := range weeks {
		w.tokens -= earliest[b].Total()
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start.Before(out[j].start) })
	return out
}

// Spike explainer

type dayTotal struct {
	day    time.Time
	tokens int64
}

// dayTotals returns the tokens of every day from from to to.
func dayTotals(usage []Usage, from, to time.Time) []dayTotal {
	var days []dayTotal
	index := map[time.Time]int{}
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		index[d] = len(days)
		days = append(days, dayTotal{day: d})
	}
	for _, u := range usage {
		if i, ok := index[day(u.Time)]; ok {
			days[i].tokens += u.Total()
		}
	}
	return days
}

// share is a name and its part of a total.
type share struct {
	name string
	part float64
}

// dayReport is what drove the tokens of one day.
type dayReport struct {
	total, median           int64   // tokens of the day and of the median day of the range
	sessions                []share // the sessions that used the most
	context, rangeContext   int64   // median context of a call on the day and in the range
	idle, other             int     // cache misses after more than 1 hour idle, and the rest
	idleTokens, otherTokens int64
	sub                     float64 // part of the tokens that subagents used
	heaviest                *Output // the output with the most rent, or nil
	models                  []share
}

// explainDay says what drove the tokens of one day, next to the median day
// of the range.
func explainDay(d time.Time, days []dayTotal, usage []Usage, outputs []Output, titles map[string]string) dayReport {
	var r dayReport
	var dayUsage []Usage
	var contexts, allContexts []int64
	for _, u := range usage {
		allContexts = append(allContexts, u.Context)
		if day(u.Time).Equal(d) {
			dayUsage = append(dayUsage, u)
			contexts = append(contexts, u.Context)
			r.total += u.Total()
		}
	}
	if r.total == 0 {
		return r
	}
	totals := make([]int64, len(days))
	for i, t := range days {
		totals[i] = t.tokens
	}
	r.median, r.context, r.rangeContext = median(totals), median(contexts), median(allContexts)

	sessions, models, projects := map[string]int64{}, map[string]int64{}, map[string]string{}
	var subTokens int64
	for _, u := range dayUsage {
		sessions[u.Session] += u.Total()
		projects[u.Session] = u.Project
		models[u.Model] += u.Total()
		if u.Sub {
			subTokens += u.Total()
		}
		switch {
		case u.Miss > 0 && u.Gap > time.Hour:
			r.idle++
			r.idleTokens += u.Miss
		case u.Miss > 0:
			r.other++
			r.otherTokens += u.Miss
		}
	}
	part := func(n int64) float64 { return float64(n) / float64(r.total) }
	for _, s := range topKeys(sessions, 3) {
		r.sessions = append(r.sessions, share{sessionName(titles, s, projects[s]), part(sessions[s])})
	}
	for _, name := range topKeys(models, 3) {
		r.models = append(r.models, share{name, part(models[name])})
	}
	r.sub = part(subTokens)
	for i, o := range outputs {
		if day(o.Time).Equal(d) && o.Rent() > 0 && (r.heaviest == nil || o.Rent() > r.heaviest.Rent()) {
			r.heaviest = &outputs[i]
		}
	}
	return r
}

// topKeys returns the n keys with the largest values.
func topKeys(m map[string]int64, n int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys[:min(n, len(keys))]
}

// Session timeline

type sessionInfo struct {
	key     string // log file
	project string
	tool    Tool
	start   time.Time
	tokens  int64
	calls   int
	peak    int64 // largest context
}

// sessions lists the sessions, most tokens first.
func sessions(usage []Usage) []sessionInfo {
	byKey := map[string]*sessionInfo{}
	for _, u := range usage {
		s := byKey[u.Session]
		if s == nil {
			s = &sessionInfo{key: u.Session, project: u.Project, tool: u.Tool, start: u.Time}
			byKey[u.Session] = s
		}
		if u.Time.Before(s.start) {
			s.start = u.Time
		}
		s.tokens += u.Total()
		s.calls++
		s.peak = max(s.peak, u.Context)
	}
	out := make([]sessionInfo, 0, len(byKey))
	for _, s := range byKey {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].tokens != out[j].tokens {
			return out[i].tokens > out[j].tokens
		}
		return out[i].key < out[j].key
	})
	return out
}

// sessionName is the title of a session, or its project.
func sessionName(titles map[string]string, key, project string) string {
	if t := titles[key]; t != "" {
		return t
	}
	return project
}

type event struct {
	call int    // index of the call in the session
	mark rune   // ◆ cache rebuild, ▼ compaction, ◇ subagent, ● large output
	out  Output // the output of a subagent or a large output
}

// Marks of the timeline events.
const (
	markRebuild    = '◆'
	markCompaction = '▼'
	markSubagent   = '◇'
	markOutput     = '●'
)

// markRank orders the marks. When there is not room for all events, the
// timeline keeps the marks with the higher rank.
var markRank = map[rune]int{markRebuild: 4, markCompaction: 3, markSubagent: 2, markOutput: 1}

// largeOutput is the size from which an output shows on the timeline.
const largeOutput = 20_000

// timeline returns the calls of one session in time order and what
// happened during it.
func timeline(key string, usage []Usage, outputs []Output) ([]Usage, []event) {
	var calls []Usage
	for _, u := range usage {
		if u.Session == key {
			calls = append(calls, u)
		}
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].Time.Before(calls[j].Time) })
	index := map[time.Time]int{}
	var events []event
	for i, u := range calls {
		index[u.Time] = i
		if u.Miss > 0 {
			events = append(events, event{call: i, mark: markRebuild})
		}
		if u.Compact {
			events = append(events, event{call: i, mark: markCompaction})
		}
	}
	for _, o := range outputs {
		i, ok := index[o.Time]
		if o.Session != key || !ok {
			continue
		}
		switch {
		case o.Name == "Agent" || o.Name == "Task" || o.Name == "spawn_agent":
			events = append(events, event{i, markSubagent, o})
		case o.Size >= largeOutput:
			events = append(events, event{i, markOutput, o})
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].call < events[j].call })
	return calls, events
}

// Version drift

type driftRow struct {
	tool     Tool
	version  string // version group
	first    time.Time
	sessions int
	start    int64   // median context of the first call of a session
	change   float64 // change of start from the previous row, NaN for the first row
	share1h  float64 // share of main-session cache writes with the 1-hour lifetime, -1 if none
	sub1h    float64 // the same for subagents
}

// versionGroup puts close versions together: Claude Code 2.1.287 is
// 2.1.28x, and Codex 0.155.1 is 0.15x.
func versionGroup(t Tool, v string) string {
	parts := strings.Split(v, ".")
	if len(parts) < 3 {
		return v
	}
	if p := parts[1]; t == Codex && len(p) > 1 {
		return parts[0] + "." + p[:len(p)-1] + "x"
	}
	if p := parts[2]; t == Claude && len(p) > 1 {
		return parts[0] + "." + parts[1] + "." + p[:len(p)-1] + "x"
	}
	return v
}

// drift compares versions of each agent: how much context a session sends
// before its first reply, and which cache lifetime it uses.
func drift(usage []Usage) []driftRow {
	type acc struct {
		row          driftRow
		starts       []int64
		main1h, main int64
		sub1h, sub   int64
	}
	groups := map[[2]string]*acc{}
	for _, u := range usage {
		if u.Version == "" {
			continue
		}
		v := versionGroup(u.Tool, u.Version)
		k := [2]string{fmt.Sprint(u.Tool), v}
		a := groups[k]
		if a == nil {
			a = &acc{row: driftRow{tool: u.Tool, version: v, first: u.Time}}
			groups[k] = a
		}
		if u.Time.Before(a.row.first) {
			a.row.first = u.Time
		}
		if u.First && !u.Sub {
			a.row.sessions++
			a.starts = append(a.starts, u.Context)
		}
		if u.Sub {
			a.sub1h += u.Cache1h
			a.sub += u.CacheWrite
		} else {
			a.main1h += u.Cache1h
			a.main += u.CacheWrite
		}
	}
	share := func(part, all int64) float64 {
		if all == 0 {
			return -1
		}
		return float64(part) / float64(all)
	}
	var rows []driftRow
	for _, a := range groups {
		if a.row.sessions == 0 {
			continue
		}
		a.row.start = median(a.starts)
		a.row.share1h = share(a.main1h, a.main)
		a.row.sub1h = share(a.sub1h, a.sub)
		rows = append(rows, a.row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].tool != rows[j].tool {
			return rows[i].tool < rows[j].tool
		}
		return rows[i].first.Before(rows[j].first)
	})
	for i := range rows {
		rows[i].change = math.NaN()
		if i > 0 && rows[i-1].tool == rows[i].tool && rows[i-1].start > 0 {
			rows[i].change = float64(rows[i].start-rows[i-1].start) / float64(rows[i-1].start)
		}
	}
	return rows
}
