package scenario

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mozership/probe-agent/internal/llm"
)

// gqlSchemaData is the data of an answer to the introspection query: a
// schema of notes, with a mutation, an input, an enum and a deprecation.
const gqlSchemaData = `{"__schema":{
  "queryType":{"name":"Query"},"mutationType":{"name":"Mutation"},"subscriptionType":null,
  "types":[
    {"kind":"OBJECT","name":"Query","fields":[
      {"name":"note","description":"A note by its id.","args":[{"name":"id","type":{"kind":"NON_NULL","ofType":{"kind":"SCALAR","name":"ID"}}}],"type":{"kind":"OBJECT","name":"Note"}},
      {"name":"notes","args":[{"name":"first","type":{"kind":"SCALAR","name":"Int"},"defaultValue":"10"}],"type":{"kind":"NON_NULL","ofType":{"kind":"LIST","ofType":{"kind":"NON_NULL","ofType":{"kind":"OBJECT","name":"Note"}}}}}
    ]},
    {"kind":"OBJECT","name":"Mutation","fields":[
      {"name":"createNote","args":[{"name":"input","type":{"kind":"NON_NULL","ofType":{"kind":"INPUT_OBJECT","name":"NoteInput"}}}],"type":{"kind":"OBJECT","name":"Note"}}
    ]},
    {"kind":"OBJECT","name":"Note","description":"A note.\nKept in memory.","interfaces":[{"kind":"INTERFACE","name":"Node"}],"fields":[
      {"name":"id","args":[],"type":{"kind":"NON_NULL","ofType":{"kind":"SCALAR","name":"ID"}}},
      {"name":"text","args":[],"type":{"kind":"SCALAR","name":"String"}},
      {"name":"body","args":[],"type":{"kind":"SCALAR","name":"String"},"isDeprecated":true,"deprecationReason":"Use text."},
      {"name":"color","args":[],"type":{"kind":"ENUM","name":"Color"}}
    ]},
    {"kind":"INTERFACE","name":"Node","fields":[{"name":"id","args":[],"type":{"kind":"NON_NULL","ofType":{"kind":"SCALAR","name":"ID"}}}]},
    {"kind":"INPUT_OBJECT","name":"NoteInput","inputFields":[{"name":"text","type":{"kind":"NON_NULL","ofType":{"kind":"SCALAR","name":"String"}}}]},
    {"kind":"ENUM","name":"Color","enumValues":[{"name":"RED"},{"name":"BLUE","isDeprecated":true}]},
    {"kind":"UNION","name":"Item","possibleTypes":[{"kind":"OBJECT","name":"Note"}]},
    {"kind":"SCALAR","name":"Time"},
    {"kind":"SCALAR","name":"String"},
    {"kind":"OBJECT","name":"__Type","fields":[]}
  ]}}`

// fakeGraphQL answers the introspection query with a schema, or with
// introspected when it is set, and the documents of the tester with
// answers, and records them.
type fakeGraphQL struct {
	introspected *GraphQLResponse
	answers      []GraphQLResponse
	calls        []GraphQLRequest
}

func (f *fakeGraphQL) target() *GraphQL {
	return &GraphQL{URL: "https://api.test/graphql", Fixed: []string{"Authorization"}, Call: f.call}
}

func (f *fakeGraphQL) call(_ context.Context, req GraphQLRequest) (GraphQLResponse, error) {
	f.calls = append(f.calls, req)
	if req.OperationName == "IntrospectionQuery" {
		if f.introspected != nil {
			return *f.introspected, nil
		}
		return GraphQLResponse{Code: 200, Status: "200 OK", Data: gqlSchemaData}, nil
	}
	if len(f.answers) == 0 {
		return GraphQLResponse{Code: 200, Status: "200 OK", Errors: []string{`{"message":"no answer"}`}}, nil
	}
	a := f.answers[0]
	f.answers = f.answers[1:]
	return a, nil
}

func TestRun_CallsAGraphQLAPI(t *testing.T) {
	api := &fakeGraphQL{answers: []GraphQLResponse{
		{Code: 200, Status: "200 OK", Data: `{"createNote":{"id":"n1","text":"Pay hunter2-long"}}`},
	}}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "graphql_schema", map[string]any{"type": "Note"}),
			toolCall("2", "graphql_query", map[string]any{
				"query":     `mutation CreateNote($text: String!) { createNote(input: {text: $text}) { id text } }`,
				"variables": `{"text": "Pay {secret:PASSWORD}"}`,
				"headers":   map[string]any{"X-Trace": "{secret:EMAIL}"},
			}),
			toolCall("3", "finish", map[string]any{"status": "done", "summary": "Created."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "r", "evidence": []string{`"id":"n1"`, "body: String @deprecated", "the mutation fields createNote"}})},
	}}
	cfg := apiConfig()
	cfg.Secrets["PASSWORD"] = `hunter2-long`
	o, err := Run(t.Context(), cfg, Targets{GraphQL: api.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Verdict != Pass {
		t.Fatalf("verdict %s: %s", o.Verdict, o.Reason)
	}
	// The schema is read once, as the tester starts.
	if len(api.calls) != 2 || api.calls[0].OperationName != "IntrospectionQuery" {
		t.Fatalf("calls %+v", api.calls)
	}
	got := api.calls[1]
	if got.Variables != `{"text": "Pay hunter2-long"}` || got.Headers["X-Trace"] != "alice@example.com" || !strings.HasPrefix(got.Query, "mutation CreateNote") {
		t.Errorf("call %+v", got)
	}
	for _, e := range o.Evidence {
		if !e.Verified {
			t.Errorf("evidence %q is not verified", e.Quote)
		}
	}

	tester := m.chats[0]
	for _, want := range []string{"The GraphQL API at https://api.test/graphql, through the tools that start with graphql_:", "fixes the headers Authorization"} {
		if !strings.Contains(tester.opts.System, want) {
			t.Errorf("the system prompt lacks %q:\n%s", want, tester.opts.System)
		}
	}
	if !strings.HasSuffix(tester.opts.Prompt, "The GraphQL API at https://api.test/graphql has the query fields note, notes; the mutation fields createNote.") || !strings.Contains(tester.opts.Prompt, "seen in the responses of the API") {
		t.Errorf("prompt:\n%s", tester.opts.Prompt)
	}
	described := tester.turns[1].Results[0].Text
	want := "# A note.\n# Kept in memory.\ntype Note implements Node {\n  id: ID!\n  text: String\n  body: String @deprecated(reason: \"Use text.\")\n  color: Color\n}"
	if described != want {
		t.Errorf("the type:\n%s\nwant:\n%s", described, want)
	}
	answer := tester.turns[2].Results[0].Text
	if !strings.HasPrefix(answer, "mutation CreateNote -> 200 OK\n") || !strings.Contains(answer, `Data: {"createNote":{"id":"n1","text":"Pay {secret:PASSWORD}"}}`) || strings.Contains(answer, "hunter2") {
		t.Errorf("answer:\n%s", answer)
	}
	if o.Steps[1].Target != "graphql" || o.Steps[1].URL != "https://api.test/graphql" || o.Steps[1].Input["variables"] != `{"text": "Pay {secret:PASSWORD}"}` {
		t.Errorf("steps %+v", o.Steps)
	}
	state := TargetState{Kind: "graphql", URL: "https://api.test/graphql", Method: "mutation CreateNote", Status: 200, Response: answer}
	if len(o.Targets) != 1 || o.Targets[0] != state {
		t.Errorf("targets %+v", o.Targets)
	}
	if judge := m.chats[1].opts.Prompt; !strings.Contains(judge, "The last response of the GraphQL API https://api.test/graphql:\n<last_response>\nmutation CreateNote -> 200 OK") {
		t.Errorf("judge prompt:\n%s", judge)
	}
}

func TestGraphQLSchema_SDL(t *testing.T) {
	s, err := readSchema(GraphQLResponse{Code: 200, Data: gqlSchemaData})
	if err != nil {
		t.Fatal(err)
	}
	sdl := s.sdl()
	for _, want := range []string{
		"schema {\n  query: Query\n  mutation: Mutation\n}\n\ntype Query {",
		"  # A note by its id.\n  note(id: ID!): Note\n  notes(first: Int = 10): [Note!]!\n}\n\ntype Mutation {\n  createNote(input: NoteInput!): Note\n}",
		"enum Color {\n  RED\n  BLUE @deprecated\n}",
		"input NoteInput {\n  text: String!\n}",
		"interface Node {\n  id: ID!\n}",
		"union Item = Note",
		"scalar Time",
	} {
		if !strings.Contains(sdl, want) {
			t.Errorf("the SDL lacks %q:\n%s", want, sdl)
		}
	}
	// Neither the types of introspection nor the built-in scalars are told.
	for _, not := range []string{"__Type", "scalar String"} {
		if strings.Contains(sdl, not) {
			t.Errorf("the SDL has %q:\n%s", not, sdl)
		}
	}
}

// An API that answers no introspection is still called, and the tester is
// told why the schema could not be read, which graphql_schema tries again.
func TestRun_GraphQLWithoutIntrospection(t *testing.T) {
	api := &fakeGraphQL{
		introspected: &GraphQLResponse{Code: 200, Status: "200 OK", Errors: []string{`{"message":"introspection is disabled"}`}},
		answers:      []GraphQLResponse{{Code: 400, Status: "400 Bad Request", Body: "bad request"}},
	}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "graphql_schema", map[string]any{}),
			toolCall("2", "graphql_query", map[string]any{"query": "{ notes { id } }"}),
			toolCall("3", "graphql_query", map[string]any{"query": " "}),
			toolCall("4", "graphql_query", map[string]any{"query": "{ a }", "variables": "[1]"}),
			toolCall("5", "graphql_query", map[string]any{"query": "{ a }", "headers": map[string]any{"authorization": "x"}}),
			toolCall("6", "graphql_query", map[string]any{"query": `{ a(k: "{secret:NOPE}") }`}),
			toolCall("7", "finish", map[string]any{"status": "done", "summary": "Done."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "fail", "reason": "r", "evidence": []string{}})},
	}}
	o, err := Run(t.Context(), apiConfig(), Targets{GraphQL: api.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(m.chats[0].opts.Prompt, `The schema of the GraphQL API at https://api.test/graphql could not be read: the introspection query was answered 200 OK with the errors {"message":"introspection is disabled"}.`) {
		t.Errorf("prompt:\n%s", m.chats[0].opts.Prompt)
	}
	turns := m.chats[0].turns
	if res := turns[1].Results[0]; !res.IsError || !strings.Contains(res.Text, "the schema could not be read") {
		t.Errorf("schema: %+v", res)
	}
	if res := turns[2].Results[0]; res.IsError || res.Text != "query -> 400 Bad Request\n\nThe body is not JSON:\nbad request" {
		t.Errorf("query: %+v", res)
	}
	for i, want := range []string{"needs a query", "the variables must be a JSON object", "the header authorization is fixed", "no secret is named NOPE"} {
		if res := turns[i+3].Results[0]; !res.IsError || !strings.Contains(res.Text, want) {
			t.Errorf("call %d: %+v, want %q", i+3, res, want)
		}
	}
	// Two introspections, as the tester starts and as it asks, and one query.
	if len(api.calls) != 3 {
		t.Errorf("calls %+v", api.calls)
	}
	if o.Targets[0].Method != "query" || o.Targets[0].Status != 400 {
		t.Errorf("targets %+v", o.Targets)
	}
}

func TestGraphQLText_ErrorsComeFirst(t *testing.T) {
	text := graphqlText("query Notes", GraphQLResponse{Code: 200, Status: "200 OK", Data: `{"notes":null}`, Errors: []string{`{"message":"denied","path":["notes"]}`}})
	want := "query Notes -> 200 OK\n\nErrors:\n- {\"message\":\"denied\",\"path\":[\"notes\"]}\n\nData: {\"notes\":null}"
	if text != want {
		t.Errorf("text:\n%s\nwant:\n%s", text, want)
	}
}

func TestOperationOf(t *testing.T) {
	for _, c := range []struct{ query, name, want string }{
		{"{ notes { id } }", "", "query"},
		{"query { notes { id } }", "", "query"},
		{"mutation CreateNote { a }", "", "mutation CreateNote"},
		{"query A { a }\nmutation B { b }", "B", "mutation B"},
		{"query A { a }", "C", "operation C"},
	} {
		if got := operationOf(c.query, c.name); got != c.want {
			t.Errorf("operationOf(%q, %q) = %q, want %q", c.query, c.name, got, c.want)
		}
	}
}

// The variables may be given as a JSON object itself, rather than as a
// string of one.
func TestRun_GraphQLVariablesAsAnObject(t *testing.T) {
	api := &fakeGraphQL{answers: []GraphQLResponse{{Code: 200, Status: "200 OK", Data: `{"note":null}`}}}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "graphql_query", map[string]any{"query": "query N($id: ID!) { note(id: $id) { id } }", "variables": map[string]any{"id": "n1"}}),
			toolCall("2", "finish", map[string]any{"status": "done", "summary": "Done."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "fail", "reason": "r", "evidence": []string{}})},
	}}
	if _, err := Run(t.Context(), apiConfig(), Targets{GraphQL: api.target()}, m); err != nil {
		t.Fatal(err)
	}
	var vars map[string]any
	if err := json.Unmarshal([]byte(api.calls[1].Variables), &vars); err != nil || vars["id"] != "n1" {
		t.Errorf("variables %q", api.calls[1].Variables)
	}
}
