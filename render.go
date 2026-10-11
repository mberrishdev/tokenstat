package main

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// 256-color palette codes.
const (
	claudeColor = 173 // #d7875f
	codexColor  = 75  // #5fafff
	dimColor    = 244
	borderColor = 239
	errColor    = 167 // #d75f5f
)

var useColor = os.Getenv("NO_COLOR") == ""

func color(c int, s string) string {
	if !useColor || s == "" {
		return s
	}
	return fmt.Sprintf("\x1b[38;5;%dm%s\x1b[0m", c, s)
}

func bold(s string) string {
	if !useColor {
		return s
	}
	return "\x1b[1m" + s + "\x1b[0m"
}

var ansiCode = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func width(s string) int { return utf8.RuneCountInString(ansiCode.ReplaceAllString(s, "")) }

func padRight(s string, w int) string { return s + strings.Repeat(" ", max(0, w-width(s))) }
func padLeft(s string, w int) string  { return strings.Repeat(" ", max(0, w-width(s))) + s }

// shortPath drops leading folders until the path fits in w cells. It cuts
// the last folder name only if that is not enough.
func shortPath(p string, w int) string {
	parts := strings.Split(p, "/")
	for i := 1; utf8.RuneCountInString(p) > w && i < len(parts); i++ {
		p = "…/" + strings.Join(parts[i:], "/")
	}
	if r := []rune(p); len(r) > w {
		p = "…" + string(r[len(r)-w+1:])
	}
	return p
}

// clip cuts a command or a path to w cells. A path keeps its end, where the
// file name is.
func clip(s string, w int) string {
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~/") {
		return shortPath(s, w)
	}
	return short(s, w)
}

func human(n int64) string {
	f := float64(n)
	switch {
	case n == 0:
		return color(dimColor, "·")
	case n >= 1e12:
		return fmt.Sprintf("%.2fT", f/1e12)
	case n >= 1e9:
		return fmt.Sprintf("%.2fB", f/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1fM", f/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.1fK", f/1e3)
	}
	return fmt.Sprint(n)
}

// overflows counts the rows that panel had to cut. Tests expect none.
var overflows int

func panel(title, right string, w int, lines []string) []string {
	border := func(s string) string { return color(borderColor, s) }
	tail := ""
	if right != "" {
		tail = " " + right + " " + border("─")
	}
	fill := w - 5 - width(title) - width(tail)
	out := []string{border("╭─ ") + bold(title) + " " + border(strings.Repeat("─", fill)) + tail + border("╮")}
	for _, l := range lines {
		if width(l) > w-4 { // never break the frame, even if a row is too long
			l = short(ansiCode.ReplaceAllString(l, ""), w-4)
			overflows++
		}
		out = append(out, border("│")+" "+padRight(l, w-4)+" "+border("│"))
	}
	return append(out, border("╰"+strings.Repeat("─", w-2)+"╯"))
}

// pair holds a token count per tool, indexed by Tool.
type pair [2]int64

func (p pair) sum() int64 { return p[Claude] + p[Codex] }

func (p *pair) add(u Usage) { p[u.Tool] += u.Total() }

// bar draws Claude and Codex tokens as one stacked bar, scaled so that
// scale fills w cells.
func bar(p pair, scale int64, w int) string {
	if scale <= 0 || p.sum() == 0 {
		return strings.Repeat(" ", w)
	}
	a := int(float64(p[Claude])/float64(scale)*float64(w) + 0.5)
	ab := int(float64(p.sum())/float64(scale)*float64(w) + 0.5)
	if ab == 0 {
		c := claudeColor
		if p[Codex] > p[Claude] {
			c = codexColor
		}
		return color(c, "▏") + strings.Repeat(" ", w-1)
	}
	codexChar := "█"
	if !useColor {
		codexChar = "▒"
	}
	return color(claudeColor, strings.Repeat("█", a)) +
		color(codexColor, strings.Repeat(codexChar, ab-a)) +
		strings.Repeat(" ", w-ab)
}

// chart draws one stacked bar per row with the Claude, Codex, and total
// tokens on the right.
func chart(labels []string, values []pair, highlight int, inner int) []string {
	const labelW, numW = 10, 8
	barW := inner - labelW - 3 - 3*numW
	var scale int64
	for _, v := range values {
		scale = max(scale, v.sum())
	}
	lines := []string{padRight("", labelW+2+barW+1) +
		padLeft(color(claudeColor, "Claude"), numW) + padLeft(color(codexColor, "Codex"), numW) + padLeft("Total", numW)}
	for i, v := range values {
		label := padRight(labels[i], labelW)
		if i == highlight {
			label = bold(label)
		} else if v.sum() == 0 {
			label = color(dimColor, label)
		}
		lines = append(lines, label+"  "+bar(v, scale, barW)+" "+
			padLeft(human(v[Claude]), numW)+padLeft(human(v[Codex]), numW)+padLeft(bold(human(v.sum())), numW))
	}
	return lines
}

const minWidth, minHeight, maxWidth = 80, 24, 140

func (m model) View() string {
	if !m.loaded || m.width == 0 {
		return "\n  Reading Claude Code and Codex logs…"
	}
	if m.width < minWidth || m.height < minHeight {
		return fmt.Sprintf("\n  tokenstat needs a window of at least %d×%d. This one is %d×%d.",
			minWidth, minHeight, m.width, m.height)
	}
	w := min(m.width, maxWidth)
	used, outputs := m.used()
	lines := m.summary(used, w)
	rows := m.height - len(lines) - 1
	switch {
	case m.mode == pickingProject || m.mode == pickingModel:
		lines = append(lines, m.picker(w, rows)...)
	case m.screen == dashboard:
		lines = append(lines, m.panels(used, w, rows)...)
	default:
		lines = append(lines, m.advancedView(used, outputs, w, rows)...)
	}
	lines = lines[:min(len(lines), m.height-1)]
	for len(lines) < m.height-1 {
		lines = append(lines, "")
	}
	return strings.Join(append(lines, m.footer()), "\n")
}

func dayCount(from, to time.Time) int {
	return int(math.Round(to.AddDate(0, 0, 1).Sub(from).Hours() / 24))
}

func (m model) rangeLabel() string {
	from, to := m.f.from, m.f.to
	n := dayCount(from, to)
	if n == 1 {
		return from.Format("Mon 2 Jan 2006")
	}
	start := from.Format("2 Jan")
	if from.Year() != to.Year() {
		start = from.Format("2 Jan 2006")
	}
	return fmt.Sprintf("%s – %s · %d days", start, to.Format("2 Jan 2006"), n)
}

func (m model) filterLine(inner int) string {
	tool := "All"
	switch m.f.tool {
	case int(Claude):
		tool = color(claudeColor, "Claude Code")
	case int(Codex):
		tool = color(codexColor, "Codex")
	}
	model := "All"
	if m.f.model != "" {
		model = bold(m.f.model)
	}
	head := color(dimColor, "Tool ") + tool + color(dimColor, "   Model ") + model + color(dimColor, "   Project ")
	note := ""
	if m.failed > 0 {
		note = color(errColor, fmt.Sprintf("   %d files could not be read", m.failed))
	}
	project := "All"
	if m.f.project != "" {
		project = bold(shortPath(m.f.project, inner-width(head)-width(note)))
	}
	return head + project + note
}

func (m model) summary(used []Usage, w int) []string {
	var mix [3]Usage // Claude Code, Codex, both
	for _, u := range used {
		for _, x := range []*Usage{&mix[u.Tool], &mix[2]} {
			x.Input += u.Input
			x.Output += u.Output
			x.CacheRead += u.CacheRead
			x.CacheWrite += u.CacheWrite
		}
	}
	const labelW, colW = 12, 12
	lines := []string{m.filterLine(w - 4), color(dimColor, padRight("", labelW)+padLeft("Input", colW)+
		padLeft("Output", colW)+padLeft("Cache read", colW)+padLeft("Cache write", colW)+padLeft("Total", colW))}
	for i, label := range []string{color(claudeColor, "Claude Code"), color(codexColor, "Codex"), bold("Total")} {
		u := mix[i]
		lines = append(lines, padRight(label, labelW)+padLeft(human(u.Input), colW)+padLeft(human(u.Output), colW)+
			padLeft(human(u.CacheRead), colW)+padLeft(human(u.CacheWrite), colW)+padLeft(bold(human(u.Total())), colW))
	}
	return panel("tokenstat", color(dimColor, m.rangeLabel()), w, lines)
}

// panels fills rows lines with the chart, the projects, and the models.
func (m model) panels(used []Usage, w, rows int) []string {
	projects, models := map[string]pair{}, map[string]pair{}
	var total pair
	for _, u := range used {
		p := projects[u.Project]
		p.add(u)
		projects[u.Project] = p
		q := models[u.Model]
		q.add(u)
		models[u.Model] = q
		total.add(u)
	}

	// Each panel uses two border lines and one header line.
	avail := rows - 9
	clamp := func(n, lo, hi int) int { return max(lo, min(n, hi)) }
	modelRows := clamp(len(models), 1, min(5, avail/5))
	projectMin := clamp(len(projects), 1, avail/5)
	title, bs := buckets(m.f.from, m.f.to, max(1, avail-modelRows-projectMin))
	projectRows := clamp(len(projects), 1, avail-modelRows-len(bs))

	labels := make([]string, len(bs))
	values := make([]pair, len(bs))
	today, highlight := day(time.Now()), -1
	for i, b := range bs {
		labels[i] = b.label
		if !today.Before(b.from) && today.Before(b.to) {
			highlight = i
		}
	}
	for _, u := range used {
		if i := sort.Search(len(bs), func(i int) bool { return u.Time.Before(bs[i].to) }); i < len(bs) {
			values[i].add(u)
		}
	}

	lines := panel(title, "", w, chart(labels, values, highlight, w-4))
	lines = append(lines, table("Projects", "Project", ranked(projects), projectRows, total.sum(), w)...)
	return append(lines, table("Models", "Model", ranked(models), modelRows, total.sum(), w)...)
}

// table lists the items with the most tokens and their share of all tokens.
func table(title, column string, items []item, rows int, total int64, w int) []string {
	inner := w - 4
	const colW, pctW = 9, 5
	nameW := min(36, (inner-3*colW-pctW-2)/2)
	barW := inner - nameW - 3*colW - pctW - 2
	lines := []string{color(dimColor, padRight(column, nameW)) + padLeft(color(claudeColor, "Claude"), colW) +
		padLeft(color(codexColor, "Codex"), colW) + color(dimColor, padLeft("Total", colW)+"  "+padRight("", barW)+padLeft("share", pctW))}
	if len(items) == 0 {
		lines = append(lines, color(dimColor, "No usage for these filters"))
	}
	for _, it := range items[:min(rows, len(items))] {
		pct := fmt.Sprintf("%.0f%%", 100*float64(it.tokens.sum())/float64(total))
		lines = append(lines, padRight(shortPath(it.name, nameW-1), nameW)+padLeft(human(it.tokens[Claude]), colW)+
			padLeft(human(it.tokens[Codex]), colW)+padLeft(bold(human(it.tokens.sum())), colW)+"  "+
			bar(it.tokens, items[0].tokens.sum(), barW)+padLeft(pct, pctW))
	}
	right := ""
	if len(items) > rows {
		right = color(dimColor, fmt.Sprintf("top %d of %d", rows, len(items)))
	}
	return panel(title, right, w, lines)
}

func (m model) picker(w, rows int) []string {
	items := m.pickItems()
	what, all := "project", "All projects"
	if m.mode == pickingModel {
		what, all = "model", "All models"
	}
	inner := w - 4
	listRows := max(1, rows-3)
	start := max(0, m.cursor-listRows+1)
	lines := []string{color(dimColor, "Search ") + m.input + "▏"}
	for i := start; i < min(len(items), start+listRows); i++ {
		name := items[i].name
		if name == "" {
			name = all
		}
		row := padRight(shortPath(name, inner-14), inner-12) + padLeft(human(items[i].tokens.sum()), 10)
		if i == m.cursor {
			row = color(claudeColor, "› ") + bold(row)
		} else {
			row = "  " + row
		}
		lines = append(lines, row)
	}
	found := len(items)
	if m.input == "" {
		found-- // the "All" item
	}
	return panel("Pick a "+what, color(dimColor, fmt.Sprintf("%d found", found)), w, lines)
}

func (m model) footer() string {
	if m.mode == typingRange {
		hint := color(dimColor, "   YYYY-MM-DD [YYYY-MM-DD] · enter apply · esc cancel")
		if m.inputErr != "" {
			hint = "   " + color(errColor, m.inputErr)
		}
		return " " + bold("Range ") + m.input + "▏" + hint
	}
	keys := [][2]string{{"d", "range"}, {"c", "custom"}, {"t", "tool"}, {"p", "project"}, {"m", "model"}, {"a", "advanced"}, {"r", "reset"}, {"q", "quit"}}
	switch {
	case m.mode != browsing:
		keys = [][2]string{{"type", "to search"}, {"↑↓", "move"}, {"enter", "select"}, {"esc", "cancel"}}
	case m.screen != dashboard:
		keys = [][2]string{{"esc", "back"}, {"↑↓", "move"}, {"enter", "open"}, {"d c t p m", "filters"}, {"r", "reset"}, {"q", "quit"}}
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, bold(k[0])+" "+color(dimColor, k[1]))
	}
	return " " + strings.Join(parts, "  ")
}
