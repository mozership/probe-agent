package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Provider is a provider of models reached through an API compatible with
// the Chat Completions API of OpenAI, or through its Responses API.
type Provider struct {
	// Name is the provider's name, such as openai.
	Name string
	// BaseURL is the URL the API's paths are under, such as
	// https://api.openai.com/v1.
	BaseURL string
	// KeyEnv is the environment variable the API key is found in.
	KeyEnv string
	// Vision is whether the provider's models are taken to see images,
	// unless told otherwise.
	Vision bool
	// Efforts maps an effort of the agent to the reasoning_effort the
	// provider takes. An effort it does not map is not sent.
	Efforts map[string]string
	// Responses is whether the provider is reached through the Responses
	// API, by which a model of OpenAI both reasons and calls tools.
	Responses bool
}

// The providers the agent knows, by name.
var Providers = map[string]Provider{
	"openai": {
		Name: "openai", BaseURL: "https://api.openai.com/v1", KeyEnv: "OPENAI_API_KEY", Vision: true, Responses: true,
		Efforts: map[string]string{"low": "low", "medium": "medium", "high": "high", "xhigh": "xhigh", "max": "xhigh"},
	},
	"xai": {
		Name: "xai", BaseURL: "https://api.x.ai/v1", KeyEnv: "XAI_API_KEY", Vision: true,
		Efforts: map[string]string{"low": "low", "medium": "medium", "high": "high", "xhigh": "xhigh", "max": "xhigh"},
	},
	"gemini": {
		Name: "gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", KeyEnv: "GEMINI_API_KEY", Vision: true,
		Efforts: map[string]string{"low": "low", "medium": "medium", "high": "high", "xhigh": "high", "max": "high"},
	},
	// The models of the Sakura AI Engine differ in what they take, so it
	// is sent no reasoning_effort, and sees no images unless told.
	"sakura": {
		Name: "sakura", BaseURL: "https://api.ai.sakura.ad.jp/v1", KeyEnv: "SAKURA_AI_ENGINE_TOKEN",
	},
}

// retries is how many times a request that failed for a while, as when
// the provider is busy or the requests of a minute are used up, is sent
// again; maxRetryWait bounds the wait before each.
const (
	retries      = 5
	maxRetryWait = time.Minute
)

// retryIn finds how long the provider asks to wait before a request is
// sent again: in Retry-After, or in the body, as Gemini says it.
var retryIn = regexp.MustCompile(`(?i)(?:retry in |"retryDelay":\s*")((?:[\d.]+h)?(?:[\d.]+m)?[\d.]+s)`)

// Compatible is a model reached through an API compatible with the Chat
// Completions API of OpenAI.
type Compatible struct {
	provider Provider
	model    string
	effort   string
	key      string
	vision   bool
	client   *http.Client

	mu    sync.Mutex
	usage Usage
}

// NewCompatible returns the model of the ID given of the provider, reached
// with the API key given, which thinks with the effort given and sees
// images when vision is true.
func NewCompatible(p Provider, model, effort, key string, vision bool) *Compatible {
	return &Compatible{provider: p, model: model, effort: effort, key: key, vision: vision, client: &http.Client{Timeout: 5 * time.Minute}}
}

// Name returns the model's ID.
func (c *Compatible) Name() string { return c.model }

// Vision says whether the model sees images.
func (c *Compatible) Vision() bool { return c.vision }

// Usage returns what every chat of the model has used so far.
func (c *Compatible) Usage() Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.usage
}

// Chat starts a chat with the model.
func (c *Compatible) Chat(opts ChatOptions) Chat {
	if c.provider.Responses {
		return c.responsesChat(opts)
	}
	ch := &compatibleChat{model: c, browser: map[string]bool{}}
	if opts.System != "" {
		ch.messages = append(ch.messages, map[string]any{"role": "system", "content": opts.System})
	}
	ch.messages = append(ch.messages, c.userMessage(opts.Prompt, opts.Images))
	if opts.Browser != nil {
		for _, t := range browserTools(opts.Browser.Disabled, c.vision) {
			ch.tools = append(ch.tools, functionTool(t))
			ch.browser[t.Name] = true
		}
	}
	for _, t := range opts.Tools {
		ch.tools = append(ch.tools, functionTool(t))
	}
	return ch
}

// userMessage is a message of the user of text and images, but for the
// images when the model sees none.
func (c *Compatible) userMessage(text string, images []Image) map[string]any {
	if !c.vision || len(images) == 0 {
		return map[string]any{"role": "user", "content": text}
	}
	parts := []any{}
	if text != "" {
		parts = append(parts, map[string]any{"type": "text", "text": text})
	}
	for _, img := range images {
		parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{
			"url": "data:" + img.MediaType + ";base64," + base64.StdEncoding.EncodeToString(img.Data),
		}})
	}
	return map[string]any{"role": "user", "content": parts}
}

func functionTool(t Tool) map[string]any {
	props := t.Properties
	if props == nil {
		props = map[string]any{}
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(t.Required) > 0 {
		schema["required"] = t.Required
	}
	return map[string]any{"type": "function", "function": map[string]any{
		"name": t.Name, "description": t.Description, "parameters": schema,
	}}
}

type compatibleChat struct {
	model    *Compatible
	messages []any
	tools    []any
	// browser are the names of the tools that are members of the browser
	// toolset.
	browser map[string]bool
	started bool
	calls   int
}

// completion is the part of a response of the Chat Completions API the
// agent reads.
type completion struct {
	Choices []struct {
		FinishReason string          `json:"finish_reason"`
		Message      json.RawMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int64 `json:"prompt_tokens"`
		CompletionTokens    int64 `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

// message is the message of a choice, as the agent reads it. The message
// goes back to the provider as it came, but for what the agent fills in,
// so that what a provider needs to see again, such as the thought
// signature Gemini puts on a tool call, is kept.
type message struct {
	Content          *string           `json:"content"`
	Refusal          *string           `json:"refusal"`
	ReasoningContent *string           `json:"reasoning_content"`
	ToolCalls        []json.RawMessage `json:"tool_calls"`
}

type toolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Send sends the prompt on the first call, and turn on each call after.
func (c *compatibleChat) Send(ctx context.Context, turn Turn) (Reply, error) {
	if c.started {
		c.messages = append(c.messages, c.turnMessages(turn)...)
	}
	c.started = true

	body := map[string]any{"model": c.model.model, "messages": c.messages}
	if len(c.tools) > 0 {
		// Every chat of the agent ends with a call, such as finish or
		// verdict: a model that is not made to call a tool can stop with
		// text instead, as gpt-oss does once it thinks the task done.
		body["tools"], body["tool_choice"] = c.tools, "required"
	}
	if e, ok := c.model.provider.Efforts[c.model.effort]; ok {
		body["reasoning_effort"] = e
	}
	var resp completion
	if err := c.model.post(ctx, "/chat/completions", body, &resp); err != nil {
		return Reply{}, err
	}
	c.model.add(resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.PromptTokensDetails.CachedTokens)
	if len(resp.Choices) == 0 {
		return Reply{}, errors.New(c.model.provider.Name + ": the response has no choices")
	}
	choice := resp.Choices[0]
	var msg message
	if err := json.Unmarshal(choice.Message, &msg); err != nil {
		return Reply{}, fmt.Errorf("%s: reading the message: %w", c.model.provider.Name, err)
	}

	reply := Reply{Stop: stopOf(choice.FinishReason)}
	if msg.Content != nil {
		reply.Text = strings.TrimSpace(*msg.Content)
	}
	if msg.Refusal != nil && *msg.Refusal != "" {
		reply.Stop, reply.Refusal = StopRefusal, *msg.Refusal
	}
	back := map[string]any{"role": "assistant", "content": reply.Text}
	if msg.ReasoningContent != nil {
		back["reasoning_content"] = *msg.ReasoningContent
	}
	var calls []any
	for _, raw := range msg.ToolCalls {
		var tc toolCall
		var echo map[string]any
		if json.Unmarshal(raw, &tc) != nil || json.Unmarshal(raw, &echo) != nil {
			continue
		}
		// A provider may give a call no ID; the result of the call needs
		// one to name it.
		c.calls++
		if tc.ID == "" {
			tc.ID = "call_" + strconv.Itoa(c.calls)
			echo["id"] = tc.ID
		}
		input := json.RawMessage(tc.Function.Arguments)
		if strings.TrimSpace(tc.Function.Arguments) == "" {
			input = json.RawMessage("{}")
			if f, ok := echo["function"].(map[string]any); ok {
				f["arguments"] = "{}"
			}
		}
		call := Call{ID: tc.ID, Name: tc.Function.Name, Input: input}
		if c.browser[call.Name] {
			call.Toolset = ToolsetBrowser
		}
		reply.Calls = append(reply.Calls, call)
		calls = append(calls, echo)
	}
	if len(calls) > 0 {
		back["tool_calls"] = calls
		if reply.Stop == StopEndTurn {
			reply.Stop = StopToolUse
		}
	}
	c.messages = append(c.messages, back)
	return reply, nil
}

func stopOf(reason string) StopReason {
	switch reason {
	case "stop", "":
		return StopEndTurn
	case "tool_calls", "function_call":
		return StopToolUse
	case "length":
		return StopMaxTokens
	case "content_filter":
		return StopRefusal
	}
	return StopReason(reason)
}

// turnMessages are the messages of a turn: a tool message for each result,
// then the images of the results and the text of the turn in a message of
// the user, since a tool message of the API carries text alone.
func (c *compatibleChat) turnMessages(turn Turn) []any {
	var out []any
	var images []Image
	var said []string
	for _, r := range turn.Results {
		text := r.Text
		if len(r.Tabs) > 0 && !r.IsError {
			for _, t := range r.Tabs {
				text = strings.TrimSpace(text + fmt.Sprintf("\nThe tab now shows %q at %s.", t.Title, t.URL))
			}
		}
		if len(r.Images) > 0 && c.model.vision {
			text = strings.TrimSpace(text + "\nThe image follows in the next message.")
			images = append(images, r.Images...)
			said = append(said, fmt.Sprintf("The image of the call %s:", r.CallID))
		}
		if text == "" {
			text = "Done."
		}
		out = append(out, map[string]any{"role": "tool", "tool_call_id": r.CallID, "content": text})
	}
	if len(images) > 0 || turn.Text != "" {
		text := strings.Join(said, "\n")
		if turn.Text != "" {
			text = strings.TrimSpace(text + "\n" + turn.Text)
		}
		out = append(out, c.model.userMessage(text, images))
	}
	return out
}

// add counts a request of input tokens, cached of them read from the
// cache, and output tokens.
func (c *Compatible) add(input, output, cached int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.usage = c.usage.Add(Usage{
		Requests:        1,
		InputTokens:     input - cached,
		OutputTokens:    output,
		CacheReadTokens: cached,
	})
}

// post sends body to the path of the API, and reads the response into
// out. A request the provider was too busy for, or failed on, is sent
// again a few times.
func (c *Compatible) post(ctx context.Context, path string, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	url := strings.TrimRight(c.provider.BaseURL, "/") + path
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.key)
		resp, err := c.client.Do(req)
		if err != nil {
			return fmt.Errorf("%s: %w", c.provider.Name, err)
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", c.provider.Name, err)
		}
		if resp.StatusCode == http.StatusOK {
			if err := json.Unmarshal(b, out); err != nil {
				return fmt.Errorf("%s: the response is not JSON: %w", c.provider.Name, err)
			}
			return nil
		}
		busy := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		wait, soon := retryWait(resp.Header.Get("Retry-After"), b, attempt)
		if !busy || !soon || attempt >= retries {
			return fmt.Errorf("%s: %s: %s", c.provider.Name, resp.Status, strings.TrimSpace(truncate(string(b), 1000)))
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// retryWait is how long to wait before the attempt after attempt: what the
// provider asks, or longer each attempt. soon is false when the provider
// asks for longer than maxRetryWait, as once the requests of a day are used
// up, which is not worth waiting for.
func retryWait(header string, body []byte, attempt int) (wait time.Duration, soon bool) {
	d := time.Duration(attempt+1) * 2 * time.Second
	if n, err := strconv.ParseFloat(header, 64); err == nil {
		d = time.Duration(n * float64(time.Second))
	} else if m := retryIn.FindSubmatch(body); m != nil {
		if asked, err := time.ParseDuration(string(m[1])); err == nil {
			// A little after the time asked, so as not to be early.
			d = asked + 500*time.Millisecond
		}
	}
	return d, d <= maxRetryWait
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
