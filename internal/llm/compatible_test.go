package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func newCompatible(t *testing.T, provider, effort string, vision bool, replies ...string) (*Compatible, *api) {
	t.Helper()
	a := &api{t: t, replies: replies}
	s := httptest.NewServer(a)
	t.Cleanup(s.Close)
	p := Providers[provider]
	p.BaseURL = s.URL + "/v1"
	return NewCompatible(p, "some-model", effort, "test-key", vision), a
}

const compatibleToolReply = `{
  "id": "chatcmpl-1", "object": "chat.completion",
  "choices": [{"index": 0, "finish_reason": "tool_calls", "message": {
    "role": "assistant", "content": "Reading the form first.",
    "tool_calls": [
      {"id": "call_a", "type": "function", "function": {"name": "read_page", "arguments": "{\"filter\":\"interactive\"}"},
       "extra_content": {"google": {"thought_signature": "sig"}}},
      {"type": "function", "function": {"name": "finish", "arguments": ""}}
    ]
  }}],
  "usage": {"prompt_tokens": 100, "completion_tokens": 20, "prompt_tokens_details": {"cached_tokens": 40}}
}`

const compatibleEndReply = `{
  "choices": [{"index": 0, "finish_reason": "stop", "message": {"role": "assistant", "content": "Done."}}],
  "usage": {"prompt_tokens": 10, "completion_tokens": 2}
}`

func TestCompatible_FirstRequest(t *testing.T) {
	m, a := newCompatible(t, "xai", "max", true, compatibleToolReply)
	chat := m.Chat(ChatOptions{
		System:  "be a tester",
		Prompt:  "do the task",
		Images:  []Image{{MediaType: "image/jpeg", Data: []byte{1, 2}}},
		Tools:   []Tool{{Name: "finish", Description: "end", Properties: map[string]any{"status": map[string]any{"type": "string"}}, Required: []string{"status"}}},
		Browser: &BrowserToolset{Disabled: []string{"javascript_exec", "hover"}},
	})
	reply, err := chat.Send(context.Background(), Turn{})
	if err != nil {
		t.Fatal(err)
	}

	if got := a.headers[0].Get("Authorization"); got != "Bearer test-key" {
		t.Errorf("Authorization %q", got)
	}
	req := a.requests[0]
	if req["model"] != "some-model" || req["reasoning_effort"] != "xhigh" || req["tool_choice"] != "required" {
		t.Errorf("model %v, reasoning_effort %v, tool_choice %v; want some-model, xhigh, required", req["model"], req["reasoning_effort"], req["tool_choice"])
	}
	body, _ := json.Marshal(req)
	for _, want := range []string{
		`{"content":"be a tester","role":"system"}`,
		`"image_url":{"url":"data:image/jpeg;base64,AQI="}`,
		`"name":"read_page"`, `"name":"screenshot"`, `"name":"finish"`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the request lacks %s:\n%s", want, body)
		}
	}
	if strings.Contains(string(body), `"name":"hover"`) {
		t.Errorf("the request offers a disabled member:\n%s", body)
	}

	if reply.Stop != StopToolUse || reply.Text != "Reading the form first." || len(reply.Calls) != 2 {
		t.Fatalf("reply %+v", reply)
	}
	if c := reply.Calls[0]; c.ID != "call_a" || c.Toolset != ToolsetBrowser || string(c.Input) != `{"filter":"interactive"}` {
		t.Errorf("first call %+v", c)
	}
	if c := reply.Calls[1]; c.ID == "" || c.Toolset != "" || string(c.Input) != "{}" {
		t.Errorf("a call without an ID or arguments came as %+v", c)
	}
	if u := m.Usage(); u != (Usage{Requests: 1, InputTokens: 60, OutputTokens: 20, CacheReadTokens: 40}) {
		t.Errorf("usage %+v", u)
	}
}

func TestCompatible_ResultsGoBackWithTheReply(t *testing.T) {
	m, a := newCompatible(t, "gemini", "medium", true, compatibleToolReply, compatibleEndReply)
	chat := m.Chat(ChatOptions{Prompt: "go", Browser: &BrowserToolset{}})
	first, err := chat.Send(context.Background(), Turn{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = chat.Send(context.Background(), Turn{
		Results: []Result{
			{CallID: "call_a", Text: "Clicked.", Images: []Image{{MediaType: "image/jpeg", Data: []byte{3}}}, Tabs: []Tab{{ID: "tab-1", Title: "Home", URL: "http://app.test/", Active: true}}},
			{CallID: first.Calls[1].ID, Text: "Error: no", IsError: true},
		},
		Text: "Carry on.",
	})
	if err != nil {
		t.Fatal(err)
	}
	msgs, _ := a.requests[1]["messages"].([]any)
	if len(msgs) != 5 {
		t.Fatalf("%d messages; want the prompt, the reply, two results and the images:\n%v", len(msgs), msgs)
	}
	body, _ := json.Marshal(msgs[1:])
	for _, want := range []string{
		// The reply goes back with what the provider put on its calls.
		`"thought_signature":"sig"`,
		`"id":"` + first.Calls[1].ID + `"`,
		`{"content":"Clicked.\nThe tab now shows \"Home\" at http://app.test/.\nThe image follows in the next message.","role":"tool","tool_call_id":"call_a"}`,
		`{"content":"Error: no","role":"tool","tool_call_id":"` + first.Calls[1].ID + `"}`,
		`"text":"The image of the call call_a:\nCarry on."`,
		`"url":"data:image/jpeg;base64,Aw=="`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the messages lack %s:\n%s", want, body)
		}
	}
	if req := a.requests[1]; req["reasoning_effort"] != "medium" {
		t.Errorf("reasoning_effort %v", req["reasoning_effort"])
	}
}

func TestCompatible_AModelThatSeesNoImages(t *testing.T) {
	m, a := newCompatible(t, "sakura", "high", false, compatibleToolReply, compatibleEndReply)
	chat := m.Chat(ChatOptions{
		Prompt:  "judge",
		Images:  []Image{{MediaType: "image/jpeg", Data: []byte{1}}},
		Browser: &BrowserToolset{},
	})
	if _, err := chat.Send(context.Background(), Turn{}); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.Send(context.Background(), Turn{Results: []Result{{CallID: "call_a", Images: []Image{{MediaType: "image/jpeg", Data: []byte{3}}}}}}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(a.requests[1])
	for _, unwanted := range []string{"image_url", `"name":"screenshot"`, `"name":"zoom"`, `["ref","coordinate"]`, "or a point", "reasoning_effort", `"arguments":""`} {
		if strings.Contains(string(body), unwanted) {
			t.Errorf("the request has %s:\n%s", unwanted, body)
		}
	}
	msgs, _ := a.requests[1]["messages"].([]any)
	if last, _ := msgs[len(msgs)-1].(map[string]any); last["role"] != "tool" || last["content"] != "Done." {
		t.Errorf("the last message %v; want a tool message that says Done.", last)
	}
}

func TestCompatible_RefusalAndLength(t *testing.T) {
	m, _ := newCompatible(t, "xai", "medium", true,
		`{"choices": [{"finish_reason": "stop", "message": {"role": "assistant", "content": null, "refusal": "I can't help with that."}}]}`,
		`{"choices": [{"finish_reason": "length", "message": {"role": "assistant", "content": "Half"}}]}`,
	)
	r, err := m.Chat(ChatOptions{Prompt: "a"}).Send(context.Background(), Turn{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Stop != StopRefusal || r.Refusal != "I can't help with that." {
		t.Errorf("reply %+v; want a refusal", r)
	}
	r, err = m.Chat(ChatOptions{Prompt: "b"}).Send(context.Background(), Turn{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Stop != StopMaxTokens {
		t.Errorf("stop %s; want max_tokens", r.Stop)
	}
}

func TestCompatible_AnErrorSaysWhatTheProviderSaid(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"error": {"message": "Unsupported parameter: reasoning_effort"}}`)
	}))
	t.Cleanup(s.Close)
	p := Providers["openai"]
	p.BaseURL = s.URL
	_, err := NewCompatible(p, "m", "low", "k", true).Chat(ChatOptions{Prompt: "a"}).Send(context.Background(), Turn{})
	if err == nil || !strings.Contains(err.Error(), "Unsupported parameter: reasoning_effort") || !strings.HasPrefix(err.Error(), "openai: 400") {
		t.Errorf("error %v", err)
	}
}

func TestBrowserTools_MatchWhatTheAgentRuns(t *testing.T) {
	var names []string
	for _, tool := range browserTools(nil, true) {
		names = append(names, tool.Name)
		if _, err := json.Marshal(functionTool(tool)); err != nil {
			t.Errorf("%s: %v", tool.Name, err)
		}
	}
	want := []string{"navigate", "read_page", "find", "get_page_text", "screenshot", "zoom", "left_click", "right_click",
		"double_click", "triple_click", "hover", "scroll", "scroll_to", "type", "key", "form_input", "wait"}
	if !slices.Equal(names, want) {
		t.Errorf("tools %v; want %v", names, want)
	}
}

func TestRetryWait(t *testing.T) {
	for _, c := range []struct {
		header string
		body   string
		want   time.Duration
		soon   bool
	}{
		{"", "", 2 * time.Second, true},
		{"7", "", 7 * time.Second, true},
		{"", `"message": "Please retry in 3.25s."`, 3750 * time.Millisecond, true},
		{"", `"retryDelay": "21s"`, 21500 * time.Millisecond, true},
		{"", `"retryDelay": "300s"`, 300500 * time.Millisecond, false},
		{"", `"message": "Please retry in 6h21.5s."`, 6*time.Hour + 22*time.Second, false},
	} {
		got, soon := retryWait(c.header, []byte(c.body), 0)
		if got != c.want || soon != c.soon {
			t.Errorf("retryWait(%q, %q) = %s, %v; want %s, %v", c.header, c.body, got, soon, c.want, c.soon)
		}
	}
}
