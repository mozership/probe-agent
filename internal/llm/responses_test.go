package llm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const responsesToolReply = `{
  "id": "resp_1", "object": "response", "status": "completed",
  "output": [
    {"type": "reasoning", "id": "rs_1", "summary": [], "encrypted_content": "enc"},
    {"type": "message", "id": "msg_1", "role": "assistant", "content": [{"type": "output_text", "text": "Reading the form first."}]},
    {"type": "function_call", "id": "fc_1", "call_id": "call_a", "name": "read_page", "arguments": "{\"filter\":\"interactive\"}"},
    {"type": "function_call", "id": "fc_2", "call_id": "call_b", "name": "finish", "arguments": ""}
  ],
  "usage": {"input_tokens": 100, "output_tokens": 20, "input_tokens_details": {"cached_tokens": 40}}
}`

const responsesEndReply = `{
  "status": "completed",
  "output": [{"type": "message", "content": [{"type": "output_text", "text": "Done."}]}],
  "usage": {"input_tokens": 10, "output_tokens": 2}
}`

func TestResponses_FirstRequest(t *testing.T) {
	m, a := newCompatible(t, "openai", "max", true, responsesToolReply)
	chat := m.Chat(ChatOptions{
		System:  "be a tester",
		Prompt:  "do the task",
		Images:  []Image{{MediaType: "image/jpeg", Data: []byte{1, 2}}},
		Tools:   []Tool{{Name: "finish", Description: "end", Properties: map[string]any{"status": map[string]any{"type": "string"}}, Required: []string{"status"}}},
		Browser: &BrowserToolset{Disabled: []string{"hover"}},
	})
	reply, err := chat.Send(context.Background(), Turn{})
	if err != nil {
		t.Fatal(err)
	}
	if a.paths[0] != "/v1/responses" {
		t.Errorf("path %s; want /v1/responses", a.paths[0])
	}
	body, _ := json.Marshal(a.requests[0])
	for _, want := range []string{
		`"instructions":"be a tester"`,
		`"store":false`,
		`"include":["reasoning.encrypted_content"]`,
		`"reasoning":{"effort":"xhigh"}`,
		`"tool_choice":"required"`,
		`{"image_url":"data:image/jpeg;base64,AQI=","type":"input_image"}`,
		`"name":"finish","parameters":{"properties":{"status":{"type":"string"}},"required":["status"],"type":"object"},"strict":false,"type":"function"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the request lacks %s:\n%s", want, body)
		}
	}
	if strings.Contains(string(body), `"name":"hover"`) {
		t.Errorf("the request offers a disabled member")
	}
	if reply.Stop != StopToolUse || reply.Text != "Reading the form first." || len(reply.Calls) != 2 {
		t.Fatalf("reply %+v", reply)
	}
	if c := reply.Calls[0]; c.ID != "call_a" || c.Toolset != ToolsetBrowser || string(c.Input) != `{"filter":"interactive"}` {
		t.Errorf("first call %+v", c)
	}
	if c := reply.Calls[1]; c.ID != "call_b" || c.Toolset != "" || string(c.Input) != "{}" {
		t.Errorf("second call %+v", c)
	}
	if u := m.Usage(); u != (Usage{Requests: 1, InputTokens: 60, OutputTokens: 20, CacheReadTokens: 40}) {
		t.Errorf("usage %+v", u)
	}
}

func TestResponses_ResultsGoBackWithTheReply(t *testing.T) {
	m, a := newCompatible(t, "openai", "medium", true, responsesToolReply, responsesEndReply)
	chat := m.Chat(ChatOptions{Prompt: "go", Browser: &BrowserToolset{}})
	if _, err := chat.Send(context.Background(), Turn{}); err != nil {
		t.Fatal(err)
	}
	_, err := chat.Send(context.Background(), Turn{
		Results: []Result{
			{CallID: "call_a", Text: "Clicked.", Images: []Image{{MediaType: "image/jpeg", Data: []byte{3}}}, Tabs: []Tab{{ID: "tab-1", Title: "Home", URL: "http://app.test/", Active: true}}},
			{CallID: "call_b"},
		},
		Text: "Carry on.",
	})
	if err != nil {
		t.Fatal(err)
	}
	input, _ := a.requests[1]["input"].([]any)
	if len(input) != 8 {
		t.Fatalf("%d input items; want the prompt, four output items, two outputs and the text:\n%v", len(input), input)
	}
	body, _ := json.Marshal(input)
	for _, want := range []string{
		// The output goes back as it came, its reasoning encrypted.
		`"encrypted_content":"enc"`,
		`"id":"fc_1"`,
		`{"call_id":"call_a","output":[{"text":"Clicked.\nThe tab now shows \"Home\" at http://app.test/.","type":"input_text"},{"image_url":"data:image/jpeg;base64,Aw==","type":"input_image"}],"type":"function_call_output"}`,
		`{"call_id":"call_b","output":"Done.","type":"function_call_output"}`,
		`{"content":[{"text":"Carry on.","type":"input_text"}],"role":"user"}`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the input lacks %s:\n%s", want, body)
		}
	}
}

func TestResponses_IncompleteAndRefusal(t *testing.T) {
	m, _ := newCompatible(t, "openai", "low", true,
		`{"status": "incomplete", "incomplete_details": {"reason": "max_output_tokens"}, "output": []}`,
		`{"status": "completed", "output": [{"type": "message", "content": [{"type": "refusal", "refusal": "No."}]}]}`,
	)
	r, err := m.Chat(ChatOptions{Prompt: "a"}).Send(context.Background(), Turn{})
	if err != nil || r.Stop != StopMaxTokens {
		t.Errorf("reply %+v, %v; want max_tokens", r, err)
	}
	r, err = m.Chat(ChatOptions{Prompt: "b"}).Send(context.Background(), Turn{})
	if err != nil || r.Stop != StopRefusal || r.Refusal != "No." {
		t.Errorf("reply %+v, %v; want a refusal", r, err)
	}
}
