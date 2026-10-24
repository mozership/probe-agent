package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"
)

// api is a stand-in for the Messages API: it keeps each request and answers
// with the next of replies.
type api struct {
	t        *testing.T
	replies  []string
	requests []map[string]any
	headers  []http.Header
	paths    []string
}

func (a *api) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		a.t.Errorf("request is not JSON: %v", err)
	}
	a.requests = append(a.requests, req)
	a.headers = append(a.headers, r.Header.Clone())
	a.paths = append(a.paths, r.URL.Path)
	if len(a.replies) == 0 {
		a.t.Errorf("no reply left for %s", body)
		w.WriteHeader(500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, a.replies[0])
	a.replies = a.replies[1:]
}

func newModel(t *testing.T, model string, replies ...string) (*Anthropic, *api) {
	t.Helper()
	a := &api{t: t, replies: replies}
	s := httptest.NewServer(a)
	t.Cleanup(s.Close)
	return NewAnthropic(model, "medium", option.WithBaseURL(s.URL), option.WithAPIKey("test"), option.WithMaxRetries(0)), a
}

const toolUseReply = `{
  "id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5-5",
  "content": [
    {"type": "thinking", "thinking": "Reading the form first.", "signature": "sig"},
    {"type": "tool_use", "id": "toolu_1", "name": "read_page", "toolset_name": "browser", "input": {"filter": "interactive"}},
    {"type": "tool_use", "id": "toolu_2", "name": "finish", "input": {"status": "done", "summary": "ok"}}
  ],
  "stop_reason": "tool_use", "stop_sequence": null,
  "usage": {"input_tokens": 100, "output_tokens": 20, "cache_read_input_tokens": 50, "cache_creation_input_tokens": 10}
}`

const endReply = `{
  "id": "msg_2", "type": "message", "role": "assistant", "model": "claude-opus-5-5",
  "content": [{"type": "text", "text": "Done."}],
  "stop_reason": "end_turn", "stop_sequence": null,
  "usage": {"input_tokens": 10, "output_tokens": 2, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0}
}`

const refusalReply = `{
  "id": "msg_3", "type": "message", "role": "assistant", "model": "claude-opus-5-5",
  "content": [],
  "stop_reason": "refusal", "stop_sequence": null,
  "stop_details": {"type": "refusal", "category": "cyber", "explanation": "not this"},
  "usage": {"input_tokens": 10, "output_tokens": 0, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0}
}`

func TestAnthropic_FirstRequest(t *testing.T) {
	m, a := newModel(t, "claude-opus-5-5", toolUseReply)
	chat := m.Chat(ChatOptions{
		System:  "be a tester",
		Prompt:  "do the task",
		Images:  []Image{{MediaType: "image/jpeg", Data: []byte{0xff, 0xd8}}},
		Tools:   []Tool{{Name: "finish", Description: "end", Properties: map[string]any{"status": map[string]any{"type": "string"}}, Required: []string{"status"}}},
		Browser: &BrowserToolset{Disabled: []string{"javascript_exec", "new_tab"}},
	})
	reply, err := chat.Send(context.Background(), Turn{})
	if err != nil {
		t.Fatal(err)
	}

	req := a.requests[0]
	if req["model"] != "claude-opus-5-5" || req["fallbacks"] != "default" {
		t.Errorf("model %v, fallbacks %v", req["model"], req["fallbacks"])
	}
	if cc, _ := req["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Errorf("cache_control %v; want the conversation cached", req["cache_control"])
	}
	if th, _ := req["thinking"].(map[string]any); th["type"] != "adaptive" || th["display"] != "updates" {
		t.Errorf("thinking %v", req["thinking"])
	}
	if oc, _ := req["output_config"].(map[string]any); oc["effort"] != "medium" {
		t.Errorf("output_config %v", req["output_config"])
	}
	betas := a.headers[0].Get("anthropic-beta")
	for _, b := range []string{"server-side-fallback-2026-07-01", "thinking-display-updates-2026-08-18"} {
		if !strings.Contains(betas, b) {
			t.Errorf("anthropic-beta %q lacks %s", betas, b)
		}
	}

	tools, _ := json.Marshal(req["tools"])
	for _, want := range []string{
		`"type":"browser_toolset_20260801"`,
		`"javascript_exec":{"enabled":false}`,
		`"new_tab":{"enabled":false}`,
		`"name":"finish"`,
		`"required":["status"]`,
	} {
		if !strings.Contains(string(tools), want) {
			t.Errorf("tools lack %s: %s", want, tools)
		}
	}
	msgs, _ := json.Marshal(req["messages"])
	if !strings.Contains(string(msgs), `"do the task"`) || !strings.Contains(string(msgs), `"media_type":"image/jpeg"`) {
		t.Errorf("messages %s", msgs)
	}

	if len(reply.Calls) != 2 || reply.Calls[0].Toolset != ToolsetBrowser || reply.Calls[0].Name != "read_page" || reply.Calls[1].Toolset != "" {
		t.Errorf("calls %+v", reply.Calls)
	}
	if string(reply.Calls[0].Input) != `{"filter": "interactive"}` && string(reply.Calls[0].Input) != `{"filter":"interactive"}` {
		t.Errorf("input %s", reply.Calls[0].Input)
	}
	if len(reply.Notes) != 1 || reply.Notes[0] != "Reading the form first." || reply.Stop != StopToolUse {
		t.Errorf("notes %q, stop %s", reply.Notes, reply.Stop)
	}
	if u := m.Usage(); u.Requests != 1 || u.InputTokens != 100 || u.CacheReadTokens != 50 || u.CacheWriteTokens != 10 || u.OutputTokens != 20 {
		t.Errorf("usage %+v", u)
	}
}

func TestAnthropic_ResultsGoBackWithTheReply(t *testing.T) {
	m, a := newModel(t, "claude-opus-5-5", toolUseReply, endReply)
	chat := m.Chat(ChatOptions{Prompt: "go", Browser: &BrowserToolset{}})
	if _, err := chat.Send(context.Background(), Turn{}); err != nil {
		t.Fatal(err)
	}
	reply, err := chat.Send(context.Background(), Turn{Results: []Result{
		{CallID: "toolu_1", Text: `button "Go" [ref_1]`, Tabs: []Tab{{ID: "tab-1", Title: "Home", URL: "http://a.test/", Active: true}}},
		{CallID: "toolu_2", Text: "Error: no", IsError: true, Tabs: []Tab{{ID: "tab-1"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Text != "Done." || reply.Stop != StopEndTurn {
		t.Errorf("reply %+v", reply)
	}

	msgs := a.requests[1]["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("%d messages; want the prompt, the reply and the results", len(msgs))
	}
	// The reply goes back whole, its thinking block included.
	back, _ := json.Marshal(msgs[1])
	if !strings.Contains(string(back), `"signature":"sig"`) || !strings.Contains(string(back), `"toolset_name":"browser"`) {
		t.Errorf("the reply went back as %s", back)
	}
	results, _ := json.Marshal(msgs[2])
	for _, want := range []string{
		`"tool_use_id":"toolu_1"`,
		`"toolset_name":"browser"`,
		`"type":"browser_state"`,
		`"url":"http://a.test/"`,
		`"active":true`,
		`"is_error":true`,
	} {
		if !strings.Contains(string(results), want) {
			t.Errorf("results lack %s: %s", want, results)
		}
	}
	// An error result has no browser_state, and the result of a tool of
	// the agent's own names no toolset.
	var blocks []map[string]any
	json.Unmarshal(results, &struct{ Content *[]map[string]any }{&blocks})
	if len(blocks) == 2 {
		second, _ := json.Marshal(blocks[1])
		if strings.Contains(string(second), "browser_state") || strings.Contains(string(second), "toolset_name") {
			t.Errorf("the error result of finish is %s", second)
		}
	} else {
		t.Errorf("results %s", results)
	}
}

func TestAnthropic_Refusal(t *testing.T) {
	m, _ := newModel(t, "claude-opus-5-5", refusalReply)
	reply, err := m.Chat(ChatOptions{Prompt: "go"}).Send(context.Background(), Turn{})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Stop != StopRefusal || reply.Refusal != "cyber: not this" {
		t.Errorf("reply %+v", reply)
	}
}

func TestAnthropic_OtherModelsGetNoFallbackOrUpdates(t *testing.T) {
	m, a := newModel(t, "claude-haiku-5-5", endReply)
	if _, err := m.Chat(ChatOptions{Prompt: "go"}).Send(context.Background(), Turn{}); err != nil {
		t.Fatal(err)
	}
	req := a.requests[0]
	if _, ok := req["fallbacks"]; ok {
		t.Errorf("fallbacks %v sent to a model that takes none", req["fallbacks"])
	}
	if _, ok := req["thinking"]; ok {
		t.Errorf("thinking %v sent to a model that reports no updates", req["thinking"])
	}
}
