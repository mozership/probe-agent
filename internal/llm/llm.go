// Package llm is how the agent talks to a model: a chat in which the model
// calls tools and is told what they returned. The scenario runs on these
// types alone, so that it can be tested with a model that answers from a
// script.
package llm

import (
	"context"
	"encoding/json"
)

// Model starts chats with a model and counts what they used.
type Model interface {
	// Name is the model's ID, such as claude-opus-5-5.
	Name() string
	// Vision says whether the model sees images: the images of a chat are
	// left out for one that does not, and the members of the browser
	// toolset that show the page as one are not offered it.
	Vision() bool
	Chat(opts ChatOptions) Chat
	// Usage is what every chat of the model has used so far.
	Usage() Usage
}

// Chat is one conversation with the model.
type Chat interface {
	// Send sends the prompt of the chat on the first call, and the turn on
	// each call after, and returns the model's reply.
	Send(ctx context.Context, turn Turn) (Reply, error)
}

// ChatOptions are what a chat starts with.
type ChatOptions struct {
	System string
	Prompt string
	// Images go with the prompt, after its text.
	Images []Image
	// Tools are tools of the agent's own.
	Tools []Tool
	// Browser offers the model the browser toolset, the tools of a
	// browser the model is trained to use, when it is not nil.
	Browser *BrowserToolset
}

// BrowserToolset is the browser toolset as the agent offers it.
type BrowserToolset struct {
	// Disabled are the members the model is not offered, by name.
	Disabled []string
}

// Tool is a tool of the agent's own the model may call.
type Tool struct {
	Name        string
	Description string
	// Properties is the JSON Schema of each field of the tool's input.
	Properties map[string]any
	Required   []string
}

// ToolsetBrowser is the toolset a call to a member of the browser toolset
// names.
const ToolsetBrowser = "browser"

// Call is a tool call the model made.
type Call struct {
	ID   string
	Name string
	// Toolset is ToolsetBrowser for a member of the browser toolset, and
	// empty for a Tool.
	Toolset string
	Input   json.RawMessage
}

// Image is an image the model is shown.
type Image struct {
	// MediaType is image/png or image/jpeg.
	MediaType string
	Data      []byte
}

// Tab is a tab of the browser, as a result reports it to the model.
type Tab struct {
	ID     string
	Title  string
	URL    string
	Active bool
}

// Result is what a call returned.
type Result struct {
	CallID  string
	Text    string
	Images  []Image
	IsError bool
	// Tabs, when not empty, tells the model the tabs of the browser after
	// the call, as when the call changed the page's URL or title. It is
	// left out of an error.
	Tabs []Tab
}

// Turn is what the agent says to the model after its reply: the results of
// the calls it made, and a text after them.
type Turn struct {
	Results []Result
	Text    string
}

// StopReason is why the model stopped its reply.
type StopReason string

// The reasons the agent tells apart. Any other is passed as it is.
const (
	StopEndTurn   StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopRefusal   StopReason = "refusal"
)

// Reply is the model's reply.
type Reply struct {
	// Text is the text blocks of the reply, joined.
	Text string
	// Notes are the notes the model wrote on its progress between its
	// calls, when the model reports them.
	Notes []string
	Calls []Call
	Stop  StopReason
	// Refusal says why the model declined, when Stop is StopRefusal.
	Refusal string
}

// Usage counts the requests made to the model and the tokens they used.
type Usage struct {
	Requests         int   `json:"requests"`
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

// Add returns the sum of u and v.
func (u Usage) Add(v Usage) Usage {
	return Usage{
		Requests:         u.Requests + v.Requests,
		InputTokens:      u.InputTokens + v.InputTokens,
		OutputTokens:     u.OutputTokens + v.OutputTokens,
		CacheReadTokens:  u.CacheReadTokens + v.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens + v.CacheWriteTokens,
	}
}
