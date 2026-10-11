package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// writeLogs makes a new home folder with the logs in it, at paths relative
// to it. $HOME in a log is the home folder.
func writeLogs(t *testing.T, logs map[string]string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	for path, text := range logs {
		p := filepath.Join(home, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(strings.ReplaceAll(text, "$HOME", home)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// callsOf returns the calls of one log file in time order.
func callsOf(d data, path string) []Usage {
	var out []Usage
	for _, u := range d.usage {
		if u.Session == path {
			out = append(out, u)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

func outputsOf(d data, path string) []Output {
	var out []Output
	for _, o := range d.outputs {
		if o.Session == path {
			out = append(out, o)
		}
	}
	return out
}

func claudeResponse(ts, cwd, id string, output int) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":"2026-10-%sZ","cwd":"%s","requestId":"r%s","message":{"id":"%s","usage":{"input_tokens":2,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":%d}}}`,
		ts, cwd, id, id, output) + "\n"
}

func TestLoadCountsEachClaudeResponseOnce(t *testing.T) {
	// One response written as three content-block lines. The last line has
	// the final output count. A user line and a line without usage are noise.
	// A resumed session copies the response a day later.
	home := writeLogs(t, map[string]string{
		".claude/projects/-app/a.jsonl": `{"type":"user","timestamp":"2026-10-01T10:00:00Z","cwd":"/p","message":{"role":"user","content":"hi"}}` + "\n" +
			claudeResponse("01T10:00:01", "$HOME/code/app", "msg_1", 5) +
			claudeResponse("01T10:00:02", "$HOME/code/app", "msg_1", 5) +
			claudeResponse("01T10:00:03", "$HOME/code/app", "msg_1", 40) +
			`{"type":"assistant","timestamp":"2026-10-01T10:00:04Z","cwd":"/p","requestId":"req_2","message":{"id":"msg_2"}}` + "\n",
		".claude/projects/-app/b.jsonl":      claudeResponse("02T09:00:00", "$HOME/code/app", "msg_1", 5),
		".claude/projects/-app/locked.jsonl": claudeResponse("02T09:00:00", "/locked", "msg_4", 5),
	})
	if err := os.Chmod(filepath.Join(home, ".claude/projects/-app/locked.jsonl"), 0); err != nil {
		t.Fatal(err)
	}
	d := load()
	if len(d.usage) != 1 || d.failed != 1 {
		t.Fatalf("got %d calls and %d unreadable files, want 1 and 1", len(d.usage), d.failed)
	}
	byProject := map[string]Usage{}
	for _, u := range d.usage {
		byProject[u.Project] = u
	}
	u, ok := byProject["~/code/app"]
	if !ok {
		t.Fatalf("no call in ~/code/app: %v", byProject)
	}
	if u.Total() != 2+100+1000+40 || u.Output != 40 {
		t.Errorf("total %d output %d, want 1142 and 40", u.Total(), u.Output)
	}
	if want := time.Date(2026, 10, 1, 10, 0, 1, 0, time.UTC); !u.Time.Equal(want) || u.Time.Location() != time.Local {
		t.Errorf("time %v, want %v in local time", u.Time, want)
	}
}

// codexLog is a Codex rollout: the session start, the model, and the lines.
func codexLog(id, cwd, source string, lines ...string) string {
	if source == "" {
		source = `"cli"`
	}
	return fmt.Sprintf(`{"timestamp":"2026-10-01T09:00:00Z","type":"session_meta","payload":{"id":"%s","cwd":"%s","cli_version":"0.155.1","source":%s}}`, id, cwd, source) + "\n" +
		`{"timestamp":"2026-10-01T09:00:00Z","type":"turn_context","payload":{"model":"gpt-6-astra"}}` + "\n" + strings.Join(lines, "")
}

// codexCall is a token_count event on 1 October. A negative week leaves out
// the plan limits.
func codexCall(ts string, input, cached int64, week float64, reset int64) string {
	limits := ""
	if week >= 0 {
		limits = fmt.Sprintf(`,"rate_limits":{"plan_type":"pro","primary":{"used_percent":10,"window_minutes":300,"resets_at":%d},"secondary":{"used_percent":%g,"window_minutes":10080,"resets_at":%d}}`,
			reset-400000, week, reset)
	}
	return fmt.Sprintf(`{"timestamp":"2026-10-01T%sZ","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":50,"reasoning_output_tokens":0,"total_tokens":%d}}%s}}`,
		ts, input, cached, input+50, limits) + "\n"
}

const reset1 = 1790400000

func TestLoadCodexForksAndOtherSessions(t *testing.T) {
	// The fork copies the parent's call with a new time, then makes a call
	// of its own.
	writeLogs(t, map[string]string{
		".codex/sessions/2026/10/01/a.jsonl": codexLog("a", "/parent", "",
			`{"timestamp":"2026-10-01T10:00:05Z","type":"event_msg","payload":{"type":"token_count","info":null}}`+"\n",
			codexCall("10:00:10", 1000, 800, 10, reset1),
			codexCall("10:00:11", 1000, 800, 10, reset1)),
		".codex/sessions/2026/10/01/b.jsonl": codexLog("b", "/child", "",
			codexCall("12:00:00", 1000, 800, 10, reset1),
			codexCall("12:00:30", 500, 400, 12, reset1)),
	})
	d := load()
	byProject := map[string]Usage{}
	for _, u := range d.usage {
		byProject[u.Project] = u
	}
	if len(d.usage) != 2 || len(byProject) != 2 {
		t.Fatalf("got %d calls in %v, want one each in /parent and /child", len(d.usage), byProject)
	}
	p := byProject["/parent"]
	if p.Total() != 1050 || p.Input != 200 || p.CacheRead != 800 || p.Output != 50 {
		t.Errorf("parent call = %+v, want input 200, cache read 800, output 50", p)
	}
	if want := time.Date(2026, 10, 1, 10, 0, 10, 0, time.UTC); !p.Time.Equal(want) {
		t.Errorf("parent call time %v, want %v", p.Time, want)
	}
	if c := byProject["/child"]; c.Total() != 550 {
		t.Errorf("child call total %d, want 550", c.Total())
	}
}

func TestLoadSessionFields(t *testing.T) {
	// Six calls: a Bash output grows the context, an idle gap of two hours
	// makes the cache miss, then a compaction shrinks the context.
	a := func(id, ts string, cw, cr int64, extra string) string {
		return fmt.Sprintf(`{"type":"assistant","timestamp":"2026-10-01T%sZ","cwd":"/p","sessionId":"s1","version":"2.1.281","requestId":"r%s","message":{"id":"%s","model":"claude-opus-5-5"%s,"usage":{"input_tokens":2,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":10,"cache_creation":{"ephemeral_1h_input_tokens":%d}}}}`,
			ts, id, id, extra, cw, cr, cw) + "\n"
	}
	main, sub := ".claude/projects/-p/s1.jsonl", ".claude/projects/-p/s1/subagents/agent-1.jsonl"
	home := writeLogs(t, map[string]string{
		main: a("m1", "10:00:00", 10000, 0, `,"content":[{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"npm test\nmore"}}]`) +
			`{"type":"user","timestamp":"2026-10-01T10:00:05Z","message":{"content":[{"tool_use_id":"tu_1","type":"tool_result","content":"lots of output"}]}}` + "\n" +
			a("m2", "10:00:10", 30000, 10002, "") +
			a("m3", "10:00:20", 1000, 40004, "") +
			a("m4", "12:30:00", 41006, 0, "") +
			a("m5", "12:31:00", 5000, 0, "") +
			a("m6", "12:31:30", 500, 5002, "") +
			`{"type":"ai-title","aiTitle":"Fix the tests","sessionId":"s1"}` + "\n",
		sub: a("m7", "10:00:30", 3000, 0, ""),
	})
	main, sub = filepath.Join(home, main), filepath.Join(home, sub)
	d := load()
	if d.titles[main] != "Fix the tests" || d.titles[sub] != "Fix the tests (subagent)" {
		t.Errorf("titles %q and %q", d.titles[main], d.titles[sub])
	}
	if s := callsOf(d, sub); len(s) != 1 || !s[0].Sub {
		t.Errorf("subagent calls: %+v", s)
	}
	r := callsOf(d, main)
	if len(r) != 6 {
		t.Fatalf("got %d calls, want 6", len(r))
	}
	if !r[0].First || r[1].First || r[0].Sub || r[0].Version != "2.1.281" || r[0].Cache1h != 10000 {
		t.Errorf("first call fields: %+v", r[0])
	}
	if r[1].Context != 40004 || r[1].Miss != 0 || r[2].Miss != 0 {
		t.Errorf("growth without a miss: context %d, misses %d %d", r[1].Context, r[1].Miss, r[2].Miss)
	}
	if r[3].Miss != 41006 || r[3].Gap != 2*time.Hour+29*time.Minute+40*time.Second {
		t.Errorf("idle miss: miss %d gap %v", r[3].Miss, r[3].Gap)
	}
	if r[4].Miss != 0 {
		t.Errorf("compaction counted as a miss of %d", r[4].Miss)
	}
	o := outputsOf(d, main)
	if len(o) != 1 || o[0].Name != "Bash" || o[0].Detail != "npm test" || o[0].Size != 30002 || o[0].Reads != 2 || o[0].Rent() != 60004 {
		t.Errorf("outputs %+v, want Bash npm test, size 30002, read again by 2 calls", o)
	}
}

func TestLoadCodexLimitsToolsAndTitles(t *testing.T) {
	parent, child := ".codex/sessions/2026/10/01/p1.jsonl", ".codex/archived_sessions/c1.jsonl"
	home := writeLogs(t, map[string]string{
		".codex/session_index.jsonl": `{"id":"p1","thread_name":"Plan the release"}` + "\n",
		parent:                       codexLog("p1", "/repo", "", codexCall("09:30:00", 9000, 0, 38, reset1)),
		child: codexLog("c1", "/repo", `{"subagent":{"thread_spawn":{"parent_thread_id":"p1"}}}`,
			codexCall("10:00:01", 20000, 0, 40, reset1),
			`{"timestamp":"2026-10-01T10:00:02Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"ls -la\"}"}}`+"\n",
			`{"timestamp":"2026-10-01T10:00:03Z","type":"response_item","payload":{"type":"function_call_output","output":"files"}}`+"\n",
			codexCall("10:00:04", 26000, 20000, 40, reset1)),
	})
	parent, child = filepath.Join(home, parent), filepath.Join(home, child)
	d := load()
	if d.titles[parent] != "Plan the release" || d.titles[child] != "Plan the release (subagent)" {
		t.Errorf("titles %q and %q", d.titles[parent], d.titles[child])
	}
	calls := callsOf(d, child)
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(calls))
	}
	u := calls[1]
	if !u.Sub || u.Version != "0.155.1" || u.Model != "gpt-6-astra" || u.Context != 26000 || u.Project != "/repo" {
		t.Errorf("call fields: %+v", u)
	}
	if l := u.Limit; l == nil || l.Plan != "pro" || l.Week != 40 || l.Hours5 != 10 || l.WeekReset.Unix() != reset1 {
		t.Errorf("limit: %+v", u.Limit)
	}
	if o := outputsOf(d, child); len(o) != 1 || o[0].Name != "exec_command" || o[0].Detail != "ls -la" || o[0].Size != 6000 {
		t.Errorf("outputs: %+v", o)
	}
}

func TestLoadLimitPoints(t *testing.T) {
	// Two sessions share the weekly limit. The first call of each week only
	// sets the start. A fork copies the first call later.
	reset2 := int64(reset1 + 7*24*3600)
	writeLogs(t, map[string]string{
		".codex/sessions/2026/10/01/a.jsonl": codexLog("a", "/a", "",
			codexCall("10:00:00", 1000, 0, 10, reset1),
			codexCall("10:03:00", 1003, 0, 12, reset1),
			codexCall("10:04:00", 1004, 0, 15, reset1)),
		".codex/sessions/2026/10/01/b.jsonl": codexLog("b", "/b", "",
			codexCall("10:01:00", 1001, 0, 12, reset1),
			codexCall("11:00:00", 1100, 0, 3, reset2),
			codexCall("12:00:00", 1200, 0, -1, 0)),
		".codex/sessions/2026/10/01/f.jsonl": codexLog("f", "/f", "",
			codexCall("13:00:00", 1000, 0, 10, reset1)),
	})
	d := load()
	sort.Slice(d.usage, func(i, j int) bool { return d.usage[i].Time.Before(d.usage[j].Time) })
	var got []string
	for _, u := range d.usage {
		got = append(got, fmt.Sprintf("%s %s %g", u.Project, u.Time.UTC().Format("15:04"), u.Points))
	}
	want := []string{"/a 10:00 0", "/b 10:01 2", "/a 10:03 0", "/a 10:04 3", "/b 11:00 0", "/b 12:00 0"}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Errorf("points:\n got %v\nwant %v", got, want)
	}
}
