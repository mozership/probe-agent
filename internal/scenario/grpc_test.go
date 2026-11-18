package scenario

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/mozership/probe-agent/internal/llm"
)

// fakeGRPC answers the listings and the calls of the tester, and records
// them.
type fakeGRPC struct {
	answers  []GRPCResponse
	calls    []GRPCRequest
	listed   []string
	listFail error
}

func (f *fakeGRPC) target() *GRPC {
	return &GRPC{Addr: "api.test:443", Fixed: []string{"authorization"}, Proto: true, List: f.list, Call: f.call}
}

func (f *fakeGRPC) list(_ context.Context, service string) (map[string]any, error) {
	f.listed = append(f.listed, service)
	if f.listFail != nil {
		return nil, f.listFail
	}
	if service == "" {
		return map[string]any{"services": []any{"notes.v1.Notes"}}, nil
	}
	return map[string]any{
		"methods": []any{
			map[string]any{"name": "CreateNote", "service": "notes.v1.Notes", "request": "notes.v1.Note", "response": "notes.v1.Note", "client_streaming": false, "server_streaming": false, "idempotency": "IDEMPOTENCY_UNKNOWN"},
			map[string]any{"name": "WatchNotes", "service": "notes.v1.Notes", "request": "notes.v1.Note", "response": "notes.v1.Note", "client_streaming": false, "server_streaming": true, "idempotency": "NO_SIDE_EFFECTS"},
		},
		"types": map[string]any{
			"notes.v1.Note": map[string]any{"kind": "message", "fields": []any{
				map[string]any{"name": "note_id", "json_name": "noteId", "number": 1.0, "type": "string"},
				map[string]any{"name": "text", "json_name": "text", "number": 2.0, "type": "string", "optional": true},
				map[string]any{"name": "color", "json_name": "color", "number": 3.0, "type": "notes.v1.Color", "oneof": "look"},
			}},
			"notes.v1.Color": map[string]any{"kind": "enum", "values": []any{"COLOR_UNSPECIFIED", "RED"}},
		},
	}, nil
}

func (f *fakeGRPC) call(_ context.Context, req GRPCRequest) (GRPCResponse, error) {
	f.calls = append(f.calls, req)
	if len(f.answers) == 0 {
		return GRPCResponse{Code: "UNIMPLEMENTED"}, nil
	}
	a := f.answers[0]
	f.answers = f.answers[1:]
	return a, nil
}

func TestRun_CallsAGRPCAPI(t *testing.T) {
	rpc := &fakeGRPC{answers: []GRPCResponse{
		{Code: "OK", Body: `{"noteId":"n1","text":"Pay hunter2-long"}`, Operation: "notes.v1.Notes/CreateNote"},
	}}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "grpc_describe", map[string]any{"service": "notes.v1.Notes"}),
			toolCall("2", "grpc_call", map[string]any{
				"service": "notes.v1.Notes", "method": "CreateNote",
				"body": `{"text": "Pay {secret:PASSWORD}"}`, "metadata": map[string]any{"x-trace": "{secret:EMAIL}"},
			}),
			toolCall("3", "finish", map[string]any{"status": "done", "summary": "Created."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "r", "evidence": []string{`"noteId":"n1"`}})},
	}}
	cfg := apiConfig()
	cfg.Secrets["PASSWORD"] = `hunter2-long`
	o, err := Run(t.Context(), cfg, Targets{GRPC: rpc.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Verdict != Pass {
		t.Fatalf("verdict %s: %s", o.Verdict, o.Reason)
	}
	// The services are listed as the tester starts.
	if !slices.Equal(rpc.listed, []string{"", "notes.v1.Notes"}) {
		t.Errorf("listed %q", rpc.listed)
	}
	got := rpc.calls[0]
	if got.Service != "notes.v1.Notes" || got.Method != "CreateNote" || got.Body != `{"text": "Pay hunter2-long"}` || got.Metadata["x-trace"] != "alice@example.com" {
		t.Errorf("call %+v", got)
	}

	tester := m.chats[0]
	for _, want := range []string{"The gRPC API at api.test:443, through the tools that start with grpc_:", "fixes the metadata authorization", "checked against the .proto files"} {
		if !strings.Contains(tester.opts.System, want) {
			t.Errorf("the system prompt lacks %q:\n%s", want, tester.opts.System)
		}
	}
	if !strings.HasSuffix(tester.opts.Prompt, "The gRPC API at api.test:443 has the services notes.v1.Notes.") || !strings.Contains(tester.opts.Prompt, "seen in the responses of the API") {
		t.Errorf("prompt:\n%s", tester.opts.Prompt)
	}
	described := tester.turns[1].Results[0].Text
	for _, want := range []string{
		"rpc CreateNote(notes.v1.Note) returns (notes.v1.Note)\n",
		"rpc WatchNotes(notes.v1.Note) returns (stream notes.v1.Note) // NO_SIDE_EFFECTS",
		"enum notes.v1.Color { COLOR_UNSPECIFIED, RED }",
		"message notes.v1.Note {\n  string note_id = 1; // json noteId\n  optional string text = 2;\n  notes.v1.Color color = 3; // oneof look\n}",
	} {
		if !strings.Contains(described, want) {
			t.Errorf("the description lacks %q:\n%s", want, described)
		}
	}
	answer := tester.turns[2].Results[0].Text
	if !strings.Contains(answer, "notes.v1.Notes/CreateNote -> OK") || !strings.Contains(answer, `"text":"Pay {secret:PASSWORD}"`) || strings.Contains(answer, "hunter2") {
		t.Errorf("answer:\n%s", answer)
	}
	if o.Steps[1].Target != "grpc" || o.Steps[1].Input["body"] != `{"text": "Pay {secret:PASSWORD}"}` {
		t.Errorf("steps %+v", o.Steps)
	}
	want := TargetState{Kind: "grpc", Address: "api.test:443", Method: "notes.v1.Notes/CreateNote", Code: "OK", Response: answer}
	if len(o.Targets) != 1 || o.Targets[0] != want {
		t.Errorf("targets %+v", o.Targets)
	}
	if judge := m.chats[1].opts.Prompt; !strings.Contains(judge, "The last answer of the gRPC API api.test:443:\n<last_answer>\nnotes.v1.Notes/CreateNote -> OK") {
		t.Errorf("judge prompt:\n%s", judge)
	}
}

func TestAnswerText_WithoutAStatus(t *testing.T) {
	refused := answerText("notes.v1.Notes/CreateNote", GRPCResponse{Violations: []Violation{{In: "request", Field: "$.text", Message: "text must be a string"}}})
	if !strings.Contains(refused, "-> no status: nothing was sent, as the .proto files do not allow the request") {
		t.Errorf("a request refused:\n%s", refused)
	}
	left := answerText("notes.v1.Notes/WatchNotes", GRPCResponse{Messages: []string{`{}`}})
	if !strings.Contains(left, "-> no status: the call left the stream") {
		t.Errorf("a stream left:\n%s", left)
	}
}

// .proto files that cannot be listed by are an error of the workflow, and
// the test does not run.
func TestRun_GRPCProtoThatCannotBeListed(t *testing.T) {
	rpc := &fakeGRPC{listFail: errors.New("proto.files: notes.proto:3:1: syntax error")}
	m := &fakeModel{}
	_, err := Run(t.Context(), apiConfig(), Targets{GRPC: rpc.target()}, m)
	if err == nil || !strings.Contains(err.Error(), "targets.grpc.proto: proto.files: notes.proto:3:1: syntax error") {
		t.Fatalf("err %v", err)
	}
	if len(m.chats) != 0 {
		t.Error("the model was asked")
	}
}

// A test that only lists and describes can rest its verdict on what they
// showed.
func TestRun_GRPCDiscoveryIsEvidence(t *testing.T) {
	rpc := &fakeGRPC{}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "grpc_list", map[string]any{}),
			toolCall("2", "grpc_describe", map[string]any{"service": "notes.v1.Notes"}),
			toolCall("3", "finish", map[string]any{"status": "done", "summary": "Described."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "r", "evidence": []string{"The services: notes.v1.Notes.", "rpc WatchNotes(notes.v1.Note) returns (stream notes.v1.Note)", "has the services notes.v1.Notes"}})},
	}}
	o, err := Run(t.Context(), apiConfig(), Targets{GRPC: rpc.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Verdict != Pass {
		t.Fatalf("verdict %s: %s", o.Verdict, o.Reason)
	}
	for _, e := range o.Evidence {
		if !e.Verified {
			t.Errorf("evidence %q is not verified", e.Quote)
		}
	}
}

func TestRun_GRPCCallsRefused(t *testing.T) {
	rpc := &fakeGRPC{listFail: errors.New("the server serves no reflection")}
	target := rpc.target()
	target.Proto = false
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "grpc_call", map[string]any{"service": "notes.v1.Notes"}),
			toolCall("2", "grpc_call", map[string]any{"service": "notes.v1.Notes", "method": "M", "body": "not json"}),
			toolCall("3", "grpc_call", map[string]any{"service": "notes.v1.Notes", "method": "M", "metadata": map[string]any{"Authorization": "x"}}),
			toolCall("4", "grpc_describe", map[string]any{}),
			toolCall("5", "grpc_call", map[string]any{"service": "notes.v1.Notes", "method": "M", "body": `{"a":"{secret:NOPE}"}`}),
			toolCall("6", "finish", map[string]any{"status": "done", "summary": "Done."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "fail", "reason": "r", "evidence": []string{}})},
	}}
	o, err := Run(t.Context(), apiConfig(), Targets{GRPC: target}, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(rpc.calls) != 0 {
		t.Errorf("called %+v", rpc.calls)
	}
	// A server that cannot be listed is still tested, and the tester told why.
	if !strings.HasSuffix(m.chats[0].opts.Prompt, "The services of the gRPC API at api.test:443 could not be listed: the server serves no reflection.") {
		t.Errorf("prompt:\n%s", m.chats[0].opts.Prompt)
	}
	for i, want := range []string{"needs a service and a method", "the body must be JSON", "the metadata Authorization is fixed", "grpc_describe needs a service", "no secret is named NOPE"} {
		if res := m.chats[0].turns[i+1].Results[0]; !res.IsError || !strings.Contains(res.Text, want) {
			t.Errorf("call %d: %+v, want %q", i+1, res, want)
		}
	}
	if o.Targets[0] != (TargetState{Kind: "grpc", Address: "api.test:443"}) {
		t.Errorf("targets %+v", o.Targets)
	}
}

// A response the .proto files do not allow is a finding of a persona test,
// and a streamed answer is shown message by message.
func TestRun_GRPCContractAndStreams(t *testing.T) {
	rpc := &fakeGRPC{answers: []GRPCResponse{
		{Code: "OK", Body: `{"noteId":1}`, Operation: "notes.v1.Notes/CreateNote", Violations: []Violation{{In: "response", Field: "$.noteId", Message: "field 1 of Note is not encoded as string note_id"}}},
		{Code: "", Messages: []string{`{"noteId":"n1"}`, `{"noteId":"n2"}`}, Body: `{"noteId":"n2"}`},
	}}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "grpc_call", map[string]any{"service": "notes.v1.Notes", "method": "CreateNote", "body": map[string]any{"text": "x"}}),
			toolCall("2", "grpc_call", map[string]any{"service": "notes.v1.Notes", "method": "WatchNotes"}),
			toolCall("3", "finish", map[string]any{"status": "done", "summary": "Done."}),
		},
		{toolCall("r1", "review", map[string]any{"verdict": "pass", "reason": "r", "evidence": []string{`{"noteId":"n2"}`}, "findings": []any{}})},
	}}
	cfg := apiConfig()
	cfg.Task, cfg.Expect = "", ""
	cfg.Persona, cfg.Goal = "A client.", "Write a note."
	o, err := Run(t.Context(), cfg, Targets{GRPC: rpc.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	// A body given as JSON itself, rather than as a string of it, is sent.
	if rpc.calls[0].Body != `{"text":"x"}` {
		t.Errorf("body %q", rpc.calls[0].Body)
	}
	streamed := m.chats[0].turns[2].Results[0].Text
	if !strings.Contains(streamed, "-> no status: the call left the stream") || !strings.Contains(streamed, "2 responses streamed:\n[0] {\"noteId\":\"n1\"}\n[1] {\"noteId\":\"n2\"}") {
		t.Errorf("streamed:\n%s", streamed)
	}
	if !strings.Contains(m.chats[1].opts.Prompt, "- step 1, gRPC notes.v1.Notes/CreateNote: in the response, $.noteId: field 1 of Note is not encoded as string note_id") {
		t.Errorf("review prompt:\n%s", m.chats[1].opts.Prompt)
	}
	if len(o.Findings) != 1 || o.Findings[0].Title != "The response of notes.v1.Notes/CreateNote breaks the .proto files at $.noteId" || !o.Findings[0].Verified || o.Findings[0].URL != "" {
		t.Errorf("findings %+v", o.Findings)
	}
}
