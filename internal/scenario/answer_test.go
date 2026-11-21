package scenario

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mozership/probe-agent/internal/llm"
)

// noteConfig is a test of an API that makes a note and reads it again.
func noteConfig() Config {
	cfg := apiConfig()
	cfg.Task = "Make a note titled Groceries, and read it again."
	cfg.Expect = "The note reads Groceries."
	return cfg
}

func noteAPI(id string) *fakeAPI {
	return &fakeAPI{responses: []HTTPResponse{
		{Code: 201, Status: "201 Created", Body: `{"id": "` + id + `", "title": "Groceries"}`},
		{Code: 200, Status: "200 OK", Body: `{"id": "` + id + `", "title": "Groceries", "done": false}`},
	}}
}

// recordedAPI runs the test of the API live with Record, and returns its
// recording.
func recordedAPI(t *testing.T) *Recording {
	t.Helper()
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "http_request", map[string]any{"method": "POST", "path": "/notes", "body": `{"title":"Groceries"}`}),
			toolCall("2", "http_request", map[string]any{"method": "GET", "path": "/notes/n-4821"}),
			toolCall("3", "finish", map[string]any{"status": "done", "summary": "Read it."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "r", "evidence": []string{`"title": "Groceries", "done": false`}})},
	}}
	cfg := noteConfig()
	cfg.Record = true
	o, err := Run(t.Context(), cfg, Targets{HTTP: noteAPI("n-4821").target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Verdict != Pass || o.Recording == nil {
		t.Fatalf("verdict %s (%s), recording %v", o.Verdict, o.Reason, o.Recording)
	}
	return o.Recording
}

func TestRun_RecordsTheCallsOfAnAPI(t *testing.T) {
	rec := recordedAPI(t)
	if rec.Start != "" || rec.Landing != "" || len(rec.Steps) != 2 {
		t.Fatalf("recording %+v", rec)
	}
	post, get := rec.Steps[0], rec.Steps[1]
	if post.Target != "http" || post.Outcome != "201" || post.Input["body"] != `{"title":"Groceries"}` || len(post.Refs) != 0 {
		t.Errorf("the POST %+v", post)
	}
	// The ID the GET took from the answer to the POST is where it was.
	if get.Outcome != "200" || get.Input["path"] != "/notes/{answer:1}" || len(get.Refs) != 1 || get.Refs[0] != (Ref{Step: 1, Key: `"id": "`, Nth: 1}) {
		t.Errorf("the GET %+v", get)
	}
	b, _ := json.Marshal(rec)
	if strings.Contains(string(b), "n-4821") {
		t.Errorf("the recording holds a value of the run: %s", b)
	}
	if rec.Checks == nil || len(rec.Checks.Quotes) != 1 {
		t.Errorf("checks %+v", rec.Checks)
	}
}

func TestRun_ReplaysTheCallsOfAnAPIWithTheValuesOfTheRun(t *testing.T) {
	rec := recordedAPI(t)
	api := noteAPI("n-9917")
	m := &fakeModel{}
	cfg := noteConfig()
	cfg.Recording, cfg.Record = rec, true
	o, err := Run(t.Context(), cfg, Targets{HTTP: api.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.chats) != 0 || o.Verdict != Pass || o.Replay.Status != ReplayHit || !o.Replay.Checked || o.Recording != nil {
		t.Fatalf("asked %d times, verdict %s (%s), replay %+v, recording %v", len(m.chats), o.Verdict, o.Reason, o.Replay, o.Recording != nil)
	}
	if len(api.requests) != 2 || api.requests[1].URL != "https://api.test/v1/notes/n-9917" {
		t.Errorf("requests %+v", api.requests)
	}
}

func TestRun_AnAnswerOtherThanTheRecordingsHandsTheTestOver(t *testing.T) {
	rec := recordedAPI(t)
	api := &fakeAPI{responses: []HTTPResponse{
		{Code: 201, Status: "201 Created", Body: `{"id": "n-5", "title": "Groceries"}`},
		{Code: 404, Status: "404 Not Found", Body: `{"error": "no note"}`},
		{Code: 200, Status: "200 OK", Body: `{"id": "n-5", "title": "Groceries", "done": false}`},
	}}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "http_request", map[string]any{"method": "GET", "path": "/notes/n-5"}),
			toolCall("2", "finish", map[string]any{"status": "done", "summary": "Read it."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "r", "evidence": []string{`"title": "Groceries", "done": false`}})},
	}}
	cfg := noteConfig()
	cfg.Recording = rec
	replayWait = 0
	t.Cleanup(func() { replayWait = 3 * time.Second })
	o, err := Run(t.Context(), cfg, Targets{HTTP: api.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Replay.Status != ReplayPartial || o.Replay.Replayed != 1 || o.Replay.DivergedAt != 2 || !o.Replay.Stale || !strings.Contains(o.Replay.Reason, `http_request got "404", where the recording got "200"`) {
		t.Fatalf("replay %+v", o.Replay)
	}
	prompt := m.chats[0].opts.Prompt
	for _, want := range []string{"1. http_request of http", `<answer step="1">`, `"id": "n-5"`, "Carry on with the task from there."} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the tester's prompt lacks %q:\n%s", want, prompt)
		}
	}
	if o.Verdict != Pass {
		t.Errorf("verdict %s", o.Verdict)
	}
}

func TestRun_ACallTheTesterSentUntilItChangedWaitsOnAReplay(t *testing.T) {
	pending := HTTPResponse{Code: 202, Status: "202 Accepted", Body: `{"state": "pending"}`}
	done := HTTPResponse{Code: 200, Status: "200 OK", Body: `{"state": "done", "rows": 12}`}
	get := func(id string) llm.Reply {
		return toolCall(id, "http_request", map[string]any{"method": "GET", "path": "/exports/latest"})
	}
	m := &fakeModel{scripts: [][]llm.Reply{
		{get("1"), get("2"), get("3"), toolCall("4", "finish", map[string]any{"status": "done", "summary": "Done."})},
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "r", "evidence": []string{`"state": "done"`}})},
	}}
	cfg := noteConfig()
	cfg.Record = true
	o, err := Run(t.Context(), cfg, Targets{HTTP: (&fakeAPI{responses: []HTTPResponse{pending, pending, done}}).target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Recording == nil || len(o.Recording.Steps) != 1 || !o.Recording.Steps[0].Wait || o.Recording.Steps[0].Outcome != "200" {
		t.Fatalf("recording %+v", o.Recording)
	}

	replayPoll = time.Millisecond
	t.Cleanup(func() { replayPoll = time.Second })
	cfg.Recording = o.Recording
	api := &fakeAPI{responses: []HTTPResponse{pending, pending, pending, done}}
	o, err = Run(t.Context(), cfg, Targets{HTTP: api.target()}, &fakeModel{})
	if err != nil {
		t.Fatal(err)
	}
	if o.Verdict != Pass || !o.Replay.Checked || len(api.requests) != 4 || len(o.Steps) != 4 {
		t.Errorf("verdict %s, replay %+v, %d requests", o.Verdict, o.Replay, len(api.requests))
	}
}

func TestBind(t *testing.T) {
	r := &run{ledger: newLedger(noteConfig())}
	r.answers = []string{
		"POST /notes -> 201 Created\nLocation: /notes/57\n\n" + `[{"id": 7, "count": 120}, {"id": 8, "count": 3}, {"id": 1}]`,
		"Your code is AB12CD9F00.",
	}
	r.texts = append(slices.Clone(r.answers), "type Address { address_line2: String }")
	for name, tc := range map[string]struct {
		in   map[string]any
		want map[string]any
		refs []Ref
		gap  string
	}{
		"a number of an identifier": {
			in:   map[string]any{"note_id": json.Number("8"), "path": "/notes/8/items?limit=120"},
			want: map[string]any{"note_id": "{answer:1}", "path": "/notes/{answer:2}/items?limit=120"},
			refs: []Ref{{Step: 1, Key: `"id": `, Nth: 2, Number: true}, {Step: 1, Key: `"id": `, Nth: 2}},
		},
		"a count an identifier happens to be": {
			in:   map[string]any{"path": "/notes?page=1", "quantity": json.Number("7")},
			want: map[string]any{"path": "/notes?page=1", "quantity": json.Number("7")},
		},
		"a segment of a path in a header": {
			in:   map[string]any{"path": "/notes/57"},
			want: map[string]any{"path": "/notes/{answer:1}"},
			refs: []Ref{{Step: 1, Key: "Location: /notes/", Nth: 1}},
		},
		"a name of a field the schema showed": {
			in:   map[string]any{"query": "{ address { address_line2 } }", "path": "/v2/jobs"},
			want: map[string]any{"query": "{ address { address_line2 } }", "path": "/v2/jobs"},
		},
		"a big number": {
			in:   map[string]any{"amount": json.Number("1234567890123456789")},
			want: map[string]any{"amount": json.Number("1234567890123456789")},
		},
		"a value of the task": {
			in:   map[string]any{"title": "Groceries"},
			want: map[string]any{"title": "Groceries"},
		},
		"a value read off an answer": {
			in:  map[string]any{"code": "AB12CD9F00"},
			gap: "it holds a value that may have been read off a target",
		},
		"a date": {
			in:  map[string]any{"due": "2026-11-21"},
			gap: "it holds a date or a time, which may have been reckoned from the date",
		},
		"a secret": {
			in:   map[string]any{"key": "{secret:PASSWORD2}"},
			want: map[string]any{"key": "{secret:PASSWORD2}"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, refs, gap := r.bind(tc.in, r.answers, r.texts, func(string) bool { return false })
			if gap != tc.gap {
				t.Fatalf("gap %q, want %q", gap, tc.gap)
			}
			if gap != "" {
				return
			}
			g, _ := json.Marshal(got)
			w, _ := json.Marshal(tc.want)
			rg, _ := json.Marshal(refs)
			rw, _ := json.Marshal(tc.refs)
			if string(g) != string(w) || string(rg) != string(rw) {
				t.Errorf("bound %s %s, want %s %s", g, rg, w, rw)
			}
		})
	}
}

func TestFill(t *testing.T) {
	answers := []string{`[{"id": 31, "count": 1}, {"id": 32, "count": 2}]`}
	refs := []Ref{{Step: 1, Key: `"id": `, Nth: 2, Number: true}, {Step: 1, Key: `"id": `, Nth: 2}}
	got, err := fill(map[string]any{"note": "{answer:1}", "path": "/notes/{answer:2}/items"}, refs, answers)
	if err != nil || got["note"] != json.Number("32") || got["path"] != "/notes/32/items" {
		t.Errorf("filled %v, %v", got, err)
	}
	if _, err := fill(map[string]any{"note": "{answer:1}"}, []Ref{{Step: 1, Key: `"uid": `, Nth: 1}}, answers); err == nil || !strings.Contains(err.Error(), `has no value after "uid": `) {
		t.Errorf("a key the answer lacks: %v", err)
	}
}

func TestRun_APollByIDWaitsOnAReplay(t *testing.T) {
	made := HTTPResponse{Code: 201, Status: "201 Created", Body: `{"id": "j-4821", "state": "pending"}`}
	pending := HTTPResponse{Code: 200, Status: "200 OK", Body: `{"id": "j-4821", "state": "pending"}`}
	done := HTTPResponse{Code: 200, Status: "200 OK", Body: `{"id": "j-4821", "state": "done"}`}
	get := func(id string) llm.Reply {
		return toolCall(id, "http_request", map[string]any{"method": "GET", "path": "/jobs/j-4821"})
	}
	m := &fakeModel{scripts: [][]llm.Reply{
		{toolCall("1", "http_request", map[string]any{"method": "POST", "path": "/jobs"}), get("2"), get("3"), toolCall("4", "finish", map[string]any{"status": "done", "summary": "Done."})},
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "r", "evidence": []string{`"state": "done"`}})},
	}}
	cfg := noteConfig()
	cfg.Record = true
	o, err := Run(t.Context(), cfg, Targets{HTTP: (&fakeAPI{responses: []HTTPResponse{made, pending, done}}).target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Recording == nil || len(o.Recording.Steps) != 2 || !o.Recording.Steps[1].Wait || o.Recording.Steps[1].Refs[0].Step != 1 {
		t.Fatalf("recording %+v", o.Recording)
	}
}

func TestChecks_TakeAnIDOfTheRunAsAnyOther(t *testing.T) {
	if checkNorm(`{"id":"n1","text":"Pay rent"}`) != checkNorm(`{"id":"n27","text":"Pay rent"}`) {
		t.Errorf("an ID tells two quotes apart")
	}
	if checkNorm("3 items") == checkNorm("0 items") {
		t.Errorf("a count does not tell two quotes apart")
	}
}

func TestChecks_TakeAHeaderOfTheRunAsAnyOther(t *testing.T) {
	if checkNorm("Content-Length: 39") != checkNorm("content-length: 41") || !strings.Contains(checkNorm("HTTP/1.1 200 OK\nContent-Length: 41\nX-Kind: note"), checkNorm("Content-Length: 39")) {
		t.Errorf("the length of a body tells two quotes apart")
	}
	if checkNorm("Content-Type: application/json") == checkNorm("Content-Type: text/html") {
		t.Errorf("a header that does not change is read as any other")
	}
}
