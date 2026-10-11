package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// testModel is a loaded model with usage, tool outputs, and Codex limits
// on the last 120 days.
func testModel(w, h int) model {
	now := time.Now()
	m := newModel()
	m.titles = map[string]string{}
	for i := range 120 {
		t := now.AddDate(0, 0, -i)
		claudeSession := fmt.Sprintf("/logs/claude-%d.jsonl", i/3)
		codexSession := fmt.Sprintf("/logs/codex-%d.jsonl", i/5)
		m.titles[claudeSession] = fmt.Sprintf("A session title that is long enough to need cutting on a narrow screen %d", i/3)
		reset := day(t).AddDate(0, 0, 7-int(day(t).Weekday()))
		for j := range 6 {
			ct := t.Add(time.Duration(-j) * 10 * time.Minute)
			m.usage = append(m.usage,
				Usage{Time: ct, Project: fmt.Sprintf("~/Development/project-%d", i%25), Tool: Claude, Model: "claude-opus-5-5",
					Session: claudeSession, Version: fmt.Sprintf("2.1.%d", 200+3*i), First: j == 5, Sub: j == 2,
					Context: int64(50_000 + 20_000*(5-j)), Miss: int64(j%3) * 30_000, Gap: time.Duration(j) * time.Hour,
					Cache1h: 9000, Tokens: Tokens{CacheRead: int64(i+1) * 100_000, CacheWrite: 10_000, Output: 5000}},
				Usage{Time: ct, Project: "~/Development/a/very/long/path/that/needs/truncation/codex-project", Tool: Codex,
					Model: fmt.Sprintf("gpt-6-astra-%d", i%7), Session: codexSession, Version: "0.155.1", First: j == 5,
					Context: int64(30_000 + 10_000*(5-j)), Points: 1,
					Limit:  &Limit{Plan: "pro", Week: float64(100 - i%7*10), WeekReset: reset, WeekResetBucket: reset.Unix() / 3600, Hours5: 20, Hours5Reset: reset},
					Tokens: Tokens{Input: 300_000, CacheRead: 2_000_000, Output: 900}})
			m.outputs = append(m.outputs, Output{Time: ct, Project: fmt.Sprintf("~/Development/project-%d", i%25), Tool: Claude,
				Model: "claude-opus-5-5", Session: claudeSession, Name: []string{"Bash", "Read", "Agent"}[j%3],
				Detail: "swift build 2>&1 | grep -E 'error|warning' && cat Sources/App/AVeryLongFileName.swift", Size: int64(25_000 * (j + 1)), Reads: 40 - j})
		}
	}
	m.loaded, m.width, m.height = true, w, h
	return m
}

// press sends keys to the model. A key longer than one character is typed
// one character at a time, except the named keys below.
func press(m model, keys ...string) model {
	for _, k := range keys {
		var msgs []tea.KeyMsg
		switch k {
		case "enter":
			msgs = []tea.KeyMsg{{Type: tea.KeyEnter}}
		case "esc":
			msgs = []tea.KeyMsg{{Type: tea.KeyEsc}}
		case "down":
			msgs = []tea.KeyMsg{{Type: tea.KeyDown}}
		case "up":
			msgs = []tea.KeyMsg{{Type: tea.KeyUp}}
		case "backspace":
			msgs = []tea.KeyMsg{{Type: tea.KeyBackspace}}
		default:
			for _, r := range k {
				if r == ' ' {
					msgs = append(msgs, tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
				} else {
					msgs = append(msgs, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
				}
			}
		}
		for _, msg := range msgs {
			next, _ := m.Update(msg)
			m = next.(model)
		}
	}
	return m
}

// clearInput deletes the text that the c key fills in.
func clearInput(m model) model {
	for range len(m.input) {
		m = press(m, "backspace")
	}
	return m
}

func TestViewFitsWindow(t *testing.T) {
	states := map[string]func(model) model{
		"14 days":      func(m model) model { return m },
		"30 days":      func(m model) model { return press(m, "d") },
		"90 days":      func(m model) model { return press(m, "d", "d") },
		"today":        func(m model) model { return press(m, "d", "d", "d") },
		"custom":       func(m model) model { return press(clearInput(press(m, "c")), "2025-11-03 2026-02-01", "enter") },
		"bad custom":   func(m model) model { return press(clearInput(press(m, "c")), "x", "enter") },
		"pick project": func(m model) model { return press(m, "p", "proj") },
		"pick model":   func(m model) model { return press(m, "m") },
		"filtered":     func(m model) model { return press(m, "t", "t", "p", "codex", "enter") },
		"no usage":     func(m model) model { return press(m, "t", "m", "gpt", "down", "enter") },
		"menu":         func(m model) model { return press(m, "a") },
		"limits":       func(m model) model { return press(m, "a", "enter") },
		"limits 90":    func(m model) model { return press(m, "d", "d", "a", "enter") },
		"spikes":       func(m model) model { return press(m, "a", "down", "enter") },
		"spikes 90":    func(m model) model { return press(m, "d", "d", "a", "down", "enter", "up", "up") },
		"rent":         func(m model) model { return press(m, "a", "down", "down", "enter") },
		"sessions":     func(m model) model { return press(m, "a", "down", "down", "down", "enter") },
		"timeline":     func(m model) model { return press(m, "a", "down", "down", "down", "enter", "down", "enter") },
		"codex line":   func(m model) model { return press(m, "t", "t", "a", "down", "down", "down", "enter", "enter") },
		"drift":        func(m model) model { return press(m, "d", "d", "a", "down", "down", "down", "down", "enter") },
		"adv no usage": func(m model) model { return press(m, "t", "m", "gpt", "down", "enter", "a", "down", "enter") },
		"adv picker":   func(m model) model { return press(m, "a", "down", "down", "enter", "p", "proj") },
	}
	for _, color := range []bool{true, false} {
		useColor = color
		for _, size := range [][2]int{{80, 24}, {100, 40}, {140, 60}, {200, 50}} {
			for name, state := range states {
				overflows = 0
				lines := strings.Split(state(testModel(size[0], size[1])).View(), "\n")
				if overflows > 0 {
					t.Errorf("%v %s: %d rows were too long for their panel", size, name, overflows)
				}
				out := strings.Join(lines, "\n")
				if open, closed := strings.Count(out, "╭"), strings.Count(out, "╰"); open != closed {
					t.Errorf("%v %s: %d panels open but %d close", size, name, open, closed)
				}
				if len(lines) != size[1] {
					t.Errorf("%v %s: %d lines, want %d", size, name, len(lines), size[1])
				}
				w := min(size[0], maxWidth)
				for _, l := range lines[:len(lines)-1] {
					if l != "" && width(l) != w {
						t.Errorf("%v %s: line is %d wide, want %d: %q", size, name, width(l), w, l)
					}
				}
				if fw := width(lines[len(lines)-1]); fw > size[0] {
					t.Errorf("%v %s: footer is %d wide", size, name, fw)
				}
			}
		}
	}
	useColor = true
}

func TestKeysChangeFilters(t *testing.T) {
	m := testModel(100, 40)
	today := day(time.Now())

	if m = press(m, "t"); m.f.tool != int(Claude) {
		t.Errorf("t: tool %d, want Claude", m.f.tool)
	}
	if m = press(m, "t", "t"); m.f.tool != -1 {
		t.Errorf("t t t: tool %d, want all", m.f.tool)
	}
	if m = press(m, "d"); dayCount(m.f.from, m.f.to) != 30 || !m.f.to.Equal(today) {
		t.Errorf("d: range %v to %v, want the last 30 days", m.f.from, m.f.to)
	}

	m = press(m, "p", "codex", "enter")
	if want := "~/Development/a/very/long/path/that/needs/truncation/codex-project"; m.f.project != want {
		t.Errorf("project search: got %q, want %q", m.f.project, want)
	}
	m = press(m, "m", "down", "enter")
	if !strings.HasPrefix(m.f.model, "gpt-6-astra-") {
		t.Errorf("model pick: got %q, want a Codex model", m.f.model)
	}
	if m = press(m, "p", "no such project", "down", "enter"); m.mode != pickingProject {
		t.Errorf("enter on an empty search result: mode %v, want the picker to stay open", m.mode)
	}
	if m = press(m, "esc"); m.mode != browsing || m.f.project == "" {
		t.Errorf("esc: mode %v project %q, want the old project kept", m.mode, m.f.project)
	}

	m = press(clearInput(press(m, "c")), "2026-01-05 2026-02-01x", "backspace", "enter")
	if m.mode != browsing || m.preset != -1 || m.f.from.Format(dateFmt) != "2026-01-05" || m.f.to.Format(dateFmt) != "2026-02-01" {
		t.Errorf("custom range: mode %v from %v to %v", m.mode, m.f.from, m.f.to)
	}
	m = press(m, "c", "9", "enter")
	if m.mode != typingRange || m.inputErr == "" {
		t.Errorf("bad custom range: mode %v error %q, want an error", m.mode, m.inputErr)
	}
	m = press(m, "esc", "r")
	if m.f.tool != -1 || m.f.project != "" || m.f.model != "" || dayCount(m.f.from, m.f.to) != 14 {
		t.Errorf("reset: filters %+v", m.f)
	}
	if _, cmd := testModel(100, 40).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}); cmd == nil {
		t.Error("q did not quit")
	}
}

func TestParseRange(t *testing.T) {
	now := time.Date(2026, 10, 10, 15, 0, 0, 0, time.Local)
	for _, c := range []struct {
		in, from, to string
		ok           bool
	}{
		{"2026-09-01", "2026-09-01", "2026-10-10", true},
		{" 2026-09-01  2026-09-30 ", "2026-09-01", "2026-09-30", true},
		{"2026-09-30 2026-09-01", "", "", false},
		{"2026-9-1", "", "", false},
		{"", "", "", false},
	} {
		from, to, err := parseRange(c.in, now)
		if (err == nil) != c.ok || (c.ok && (from.Format(dateFmt) != c.from || to.Format(dateFmt) != c.to)) {
			t.Errorf("parseRange(%q) = %v, %v, %v", c.in, from, to, err)
		}
	}
}

func TestBucketsCoverTheRange(t *testing.T) {
	from := time.Date(2026, 7, 13, 0, 0, 0, 0, time.Local)
	to := time.Date(2026, 10, 10, 0, 0, 0, 0, time.Local) // 90 days
	for _, c := range []struct {
		maxRows int
		title   string
		n       int
	}{{90, "Daily", 90}, {20, "Weekly", 13}, {5, "Monthly", 4}} {
		title, bs := buckets(from, to, c.maxRows)
		if title != c.title || len(bs) != c.n {
			t.Errorf("maxRows %d: %s with %d buckets, want %s with %d", c.maxRows, title, len(bs), c.title, c.n)
			continue
		}
		if !bs[0].from.Equal(from) || !bs[len(bs)-1].to.Equal(to.AddDate(0, 0, 1)) {
			t.Errorf("%s: buckets span %v to %v", title, bs[0].from, bs[len(bs)-1].to)
		}
		for i := 1; i < len(bs); i++ {
			if !bs[i].from.Equal(bs[i-1].to) {
				t.Errorf("%s: gap between bucket %d and %d", title, i-1, i)
			}
		}
	}
}

func TestHuman(t *testing.T) {
	useColor = false
	defer func() { useColor = true }()
	for n, want := range map[int64]string{0: "·", 999: "999", 1500: "1.5K", 2_340_000: "2.3M", 15_210_000_000: "15.21B"} {
		if got := human(n); got != want {
			t.Errorf("human(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestShortPath(t *testing.T) {
	for _, c := range []struct {
		in   string
		w    int
		want string
	}{
		{"~/Development/notes-app", 30, "~/Development/notes-app"},
		{"~/Development/notes-app", 20, "…/notes-app"},
		{"~/Development/ml-lab/training", 22, "…/ml-lab/training"},
		{"~/a/averyveryverylongprojectname", 10, "…ojectname"},
	} {
		if got := shortPath(c.in, c.w); got != c.want {
			t.Errorf("shortPath(%q, %d) = %q, want %q", c.in, c.w, got, c.want)
		}
	}
}

func TestAdvancedNavigation(t *testing.T) {
	m := press(testModel(100, 40), "a")
	if m.screen != advMenu {
		t.Fatalf("a: screen %v, want the Advanced menu", m.screen)
	}
	for i, item := range menu {
		next := m
		for range i {
			next = press(next, "down")
		}
		if next = press(next, "enter"); next.screen != item.screen {
			t.Errorf("menu row %d opened screen %v, want %v", i, next.screen, item.screen)
		}
		if next = press(next, "esc"); next.screen != advMenu || next.sel != i {
			t.Errorf("esc from %s: screen %v sel %d, want the menu on row %d", item.name, next.screen, next.sel, i)
		}
	}
	if m = press(m, "esc"); m.screen != dashboard {
		t.Errorf("esc from the menu: screen %v, want the dashboard", m.screen)
	}
	if m = press(m, "enter"); m.screen != dashboard {
		t.Errorf("enter on the dashboard opened screen %v", m.screen)
	}

	m = press(testModel(100, 40), "a", "down", "down", "down", "enter", "down", "enter")
	used, _ := m.used()
	want := sessions(used)[1].key
	if m.screen != advTimeline || m.session != want {
		t.Errorf("timeline: screen %v session %q, want the second session %q", m.screen, m.session, want)
	}
	if m = press(m, "esc"); m.screen != advSessions || m.sel != 1 {
		t.Errorf("esc from the timeline: screen %v sel %d, want the session list on row 1", m.screen, m.sel)
	}
	if m = press(m, "d"); m.screen != advSessions || dayCount(m.f.from, m.f.to) != 30 {
		t.Errorf("d on an Advanced screen: screen %v, %d days", m.screen, dayCount(m.f.from, m.f.to))
	}
	for range 100 {
		m = press(m, "down")
	}
	if n := m.listLen(); m.sel != n-1 {
		t.Errorf("down past the end: sel %d, want %d", m.sel, n-1)
	}

	m = press(testModel(100, 40), "a", "down", "enter")
	used, _ = m.used()
	days := dayTotals(used, m.f.from, m.f.to)
	for _, d := range days {
		if d.tokens > days[m.sel].tokens {
			t.Errorf("spike explainer opened on %v, not on the biggest day %v", days[m.sel].day, d.day)
		}
	}
}
