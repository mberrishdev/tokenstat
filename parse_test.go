package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestClaudeCountsEachResponseOnceWithFinalUsage(t *testing.T) {
	// One response written as three content-block lines. The last line has
	// the final output count. A user line and a line without usage are noise.
	log := `{"type":"user","timestamp":"2026-10-01T10:00:00Z","cwd":"/p","message":{"role":"user","content":"hi"}}
{"type":"assistant","timestamp":"2026-10-01T10:00:01Z","cwd":"/p","requestId":"req_1","message":{"id":"msg_1","usage":{"input_tokens":2,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":5}}}
{"type":"assistant","timestamp":"2026-10-01T10:00:02Z","cwd":"/p","requestId":"req_1","message":{"id":"msg_1","usage":{"input_tokens":2,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":5}}}
{"type":"assistant","timestamp":"2026-10-01T10:00:03Z","cwd":"/p","requestId":"req_1","message":{"id":"msg_1","usage":{"input_tokens":2,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":40}}}
{"type":"assistant","timestamp":"2026-10-01T10:00:04Z","cwd":"/p","requestId":"req_2","message":{"id":"msg_2"}}
`
	fd, err := parseClaude("x.jsonl", strings.NewReader(log))
	if err != nil {
		t.Fatal(err)
	}
	got := dedupe(fd.records)
	if len(got) != 1 {
		t.Fatalf("got %d calls, want 1", len(got))
	}
	u := got[0]
	if u.Total() != 2+100+1000+40 || u.Output != 40 {
		t.Errorf("total %d output %d, want 1142 and 40", u.Total(), u.Output)
	}
	if want := time.Date(2026, 10, 1, 10, 0, 1, 0, time.UTC); !u.Time.Equal(want) || u.Project != "/p" {
		t.Errorf("time %v project %q, want %v and /p", u.Time, u.Project, want)
	}
}

func TestCodexDropsRepeatedAndForkedEvents(t *testing.T) {
	parent := `{"timestamp":"2026-07-17T20:00:00Z","type":"session_meta","payload":{"id":"a","cwd":"/parent"}}
{"timestamp":"2026-07-17T20:00:05Z","type":"event_msg","payload":{"type":"token_count","info":null}}
{"timestamp":"2026-07-17T20:00:10Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":800,"output_tokens":50,"reasoning_output_tokens":10,"total_tokens":1050},"last_token_usage":{"input_tokens":1000,"cached_input_tokens":800,"output_tokens":50,"reasoning_output_tokens":10,"total_tokens":1050}}}}
{"timestamp":"2026-07-17T20:00:11Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":800,"output_tokens":50,"reasoning_output_tokens":10,"total_tokens":1050},"last_token_usage":{"input_tokens":1000,"cached_input_tokens":800,"output_tokens":50,"reasoning_output_tokens":10,"total_tokens":1050}}}}
`
	// The sub-agent copies the parent's event with a new time and a new
	// session total, then makes one call of its own.
	child := `{"timestamp":"2026-07-18T00:46:58Z","type":"session_meta","payload":{"id":"b","cwd":"/child"}}
{"timestamp":"2026-07-18T00:46:58Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":9000,"cached_input_tokens":800,"output_tokens":50,"reasoning_output_tokens":10,"total_tokens":9050},"last_token_usage":{"input_tokens":1000,"cached_input_tokens":800,"output_tokens":50,"reasoning_output_tokens":10,"total_tokens":1050}}}}
{"timestamp":"2026-07-18T00:47:30Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":9500,"cached_input_tokens":1200,"output_tokens":70,"reasoning_output_tokens":10,"total_tokens":9570},"last_token_usage":{"input_tokens":500,"cached_input_tokens":400,"output_tokens":20,"reasoning_output_tokens":0,"total_tokens":520}}}}
`
	var recs []record
	for _, log := range []string{child, parent} {
		fd, err := parseCodex("x.jsonl", strings.NewReader(log))
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, fd.records...)
	}
	got := dedupe(recs)
	if len(got) != 2 {
		t.Fatalf("got %d calls, want 2", len(got))
	}
	byProject := map[string]Usage{}
	for _, u := range got {
		byProject[u.Project] = u
	}
	p := byProject["/parent"]
	if p.Total() != 1050 || p.Input != 200 || p.CacheRead != 800 || p.Output != 50 {
		t.Errorf("parent call = %+v, want input 200, cache read 800, output 50", p)
	}
	if want := time.Date(2026, 7, 17, 20, 0, 10, 0, time.UTC); !p.Time.Equal(want) {
		t.Errorf("parent call time %v, want %v", p.Time, want)
	}
	if c := byProject["/child"]; c.Total() != 520 {
		t.Errorf("child call total %d, want 520", c.Total())
	}
}

func TestClaudeSessionFields(t *testing.T) {
	// Six calls: a Bash output grows the context, an idle gap of two hours
	// makes the cache miss, then a compaction shrinks the context.
	a := func(id, ts string, cw, cr int64, extra string) string {
		return fmt.Sprintf(`{"type":"assistant","timestamp":"2026-10-01T%sZ","cwd":"/p","sessionId":"s1","version":"2.1.281","requestId":"r%s","message":{"id":"%s","model":"claude-opus-5-5"%s,"usage":{"input_tokens":2,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":10,"cache_creation":{"ephemeral_1h_input_tokens":%d}}}}`,
			ts, id, id, extra, cw, cr, cw) + "\n"
	}
	log := a("m1", "10:00:00", 10000, 0, `,"content":[{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"npm test\nmore"}}]`) +
		`{"type":"user","timestamp":"2026-10-01T10:00:05Z","message":{"content":[{"tool_use_id":"tu_1","type":"tool_result","content":"lots of output"}]}}` + "\n" +
		a("m2", "10:00:10", 30000, 10002, "") +
		a("m3", "10:00:20", 1000, 40004, "") +
		a("m4", "12:30:00", 41006, 0, "") +
		a("m5", "12:31:00", 5000, 0, "") +
		a("m6", "12:31:30", 500, 5002, "") +
		`{"type":"ai-title","aiTitle":"Fix the tests","sessionId":"s1"}` + "\n"
	fd, err := parseClaude("/x/s1.jsonl", strings.NewReader(log))
	if err != nil {
		t.Fatal(err)
	}
	if fd.id != "s1" || fd.title != "Fix the tests" || len(fd.records) != 6 {
		t.Fatalf("id %q title %q records %d", fd.id, fd.title, len(fd.records))
	}
	r := fd.records
	if !r[0].First || r[1].First || r[0].Version != "2.1.281" || r[0].Cache1h != 10000 || r[0].Session != "/x/s1.jsonl" {
		t.Errorf("first call fields: %+v", r[0].Usage)
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
	if len(fd.outputs) != 1 {
		t.Fatalf("got %d outputs, want 1", len(fd.outputs))
	}
	o := fd.outputs[0]
	if o.Name != "Bash" || o.Detail != "npm test" || o.Size != 30002 || o.Reads != 2 || o.Rent() != 60004 {
		t.Errorf("output %+v, want Bash npm test, size 30002, read again by 2 calls", o)
	}
}

func TestCodexLimitsAndTools(t *testing.T) {
	tc := func(ts string, input, cached int64) string {
		return fmt.Sprintf(`{"timestamp":"2026-10-01T%sZ","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":50,"reasoning_output_tokens":0,"total_tokens":%d}},"rate_limits":{"plan_type":"pro","primary":{"used_percent":10,"window_minutes":300,"resets_at":1790000000},"secondary":{"used_percent":40,"window_minutes":10080,"resets_at":1790400000}}}}`,
			ts, input, cached, input+50) + "\n"
	}
	log := `{"timestamp":"2026-10-01T10:00:00Z","type":"session_meta","payload":{"id":"c1","cwd":"/repo","cli_version":"0.155.1","source":{"subagent":{"thread_spawn":{"parent_thread_id":"p1"}}}}}` + "\n" +
		`{"timestamp":"2026-10-01T10:00:00Z","type":"turn_context","payload":{"model":"gpt-6-astra"}}` + "\n" +
		tc("10:00:01", 20000, 0) +
		`{"timestamp":"2026-10-01T10:00:02Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"ls -la\"}"}}` + "\n" +
		`{"timestamp":"2026-10-01T10:00:03Z","type":"response_item","payload":{"type":"function_call_output","output":"files"}}` + "\n" +
		tc("10:00:04", 26000, 20000)
	fd, err := parseCodex("/c1.jsonl", strings.NewReader(log))
	if err != nil {
		t.Fatal(err)
	}
	if fd.id != "c1" || fd.parent != "p1" || len(fd.records) != 2 {
		t.Fatalf("id %q parent %q records %d", fd.id, fd.parent, len(fd.records))
	}
	u := fd.records[1]
	if !u.Sub || u.Version != "0.155.1" || u.Model != "gpt-6-astra" || u.Context != 26000 || u.Project != "/repo" {
		t.Errorf("call fields: %+v", u.Usage)
	}
	if l := u.Limit; l == nil || l.Plan != "pro" || l.Week != 40 || l.Hours5 != 10 || l.WeekReset.Unix() != 1790400000 {
		t.Errorf("limit: %+v", u.Limit)
	}
	if len(fd.outputs) != 1 || fd.outputs[0].Name != "exec_command" || fd.outputs[0].Detail != "ls -la" || fd.outputs[0].Size != 6000 {
		t.Errorf("outputs: %+v", fd.outputs)
	}
}

func TestSetLimitPoints(t *testing.T) {
	base := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	week := func(pct float64, bucket int64) *Limit { return &Limit{Week: pct, WeekResetBucket: bucket} }
	usage := []Usage{
		{Time: base.Add(3 * time.Minute), Project: "a", Limit: week(12, 1)},
		{Time: base, Project: "a", Limit: week(10, 1)},
		{Time: base.Add(time.Minute), Project: "b", Limit: week(12, 1)},
		{Time: base.Add(4 * time.Minute), Project: "a", Limit: week(15, 1)},
		{Time: base.Add(time.Hour), Project: "b", Limit: week(3, 2)},
		{Time: base.Add(2 * time.Hour), Project: "b"},
	}
	setLimitPoints(usage)
	want := []float64{0, 0, 2, 3, 0, 0}
	for i, u := range usage {
		if u.Points != want[i] {
			t.Errorf("call %d: %v points, want %v", i, u.Points, want[i])
		}
	}
}
