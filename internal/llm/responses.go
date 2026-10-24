package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The Responses API of OpenAI is how a model of it both reasons and calls
// tools. The chat keeps nothing on the provider (store is false): every
// request carries the conversation, the output of each reply as it came,
// its reasoning encrypted, so that the model reasons on from it.

type responsesChat struct {
	model        *Compatible
	instructions string
	input        []any
	tools        []any
	browser      map[string]bool
	started      bool
}

func (c *Compatible) responsesChat(opts ChatOptions) Chat {
	ch := &responsesChat{model: c, instructions: opts.System, browser: map[string]bool{}}
	ch.input = append(ch.input, c.responsesUser(opts.Prompt, opts.Images))
	if opts.Browser != nil {
		for _, t := range browserTools(opts.Browser.Disabled, c.vision) {
			ch.tools = append(ch.tools, responsesTool(t))
			ch.browser[t.Name] = true
		}
	}
	for _, t := range opts.Tools {
		ch.tools = append(ch.tools, responsesTool(t))
	}
	return ch
}

// responsesTool is t as a function tool of the Responses API, which is
// strict unless told not to be; a strict tool needs every field required.
func responsesTool(t Tool) map[string]any {
	f := functionTool(t)["function"].(map[string]any)
	f["type"], f["strict"] = "function", false
	return f
}

// responsesContent is the content of a message of text and images, but for
// the images when the model sees none.
func (c *Compatible) responsesContent(text string, images []Image) []any {
	parts := []any{}
	if text != "" {
		parts = append(parts, map[string]any{"type": "input_text", "text": text})
	}
	if c.vision {
		for _, img := range images {
			parts = append(parts, map[string]any{
				"type":      "input_image",
				"image_url": "data:" + img.MediaType + ";base64," + base64.StdEncoding.EncodeToString(img.Data),
			})
		}
	}
	return parts
}

func (c *Compatible) responsesUser(text string, images []Image) map[string]any {
	return map[string]any{"role": "user", "content": c.responsesContent(text, images)}
}

// response is the part of a response of the Responses API the agent reads.
type response struct {
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Output []json.RawMessage `json:"output"`
	Usage  struct {
		InputTokens        int64 `json:"input_tokens"`
		OutputTokens       int64 `json:"output_tokens"`
		InputTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	} `json:"usage"`
}

// outputItem is an item of the output of a response, as the agent reads it.
type outputItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
}

// Send sends the prompt on the first call, and turn on each call after.
func (c *responsesChat) Send(ctx context.Context, turn Turn) (Reply, error) {
	if c.started {
		c.input = append(c.input, c.turnItems(turn)...)
	}
	c.started = true

	body := map[string]any{
		"model":   c.model.model,
		"input":   c.input,
		"store":   false,
		"include": []string{"reasoning.encrypted_content"},
	}
	if c.instructions != "" {
		body["instructions"] = c.instructions
	}
	if len(c.tools) > 0 {
		body["tools"], body["tool_choice"] = c.tools, "required"
	}
	if e, ok := c.model.provider.Efforts[c.model.effort]; ok {
		body["reasoning"] = map[string]any{"effort": e}
	}
	var resp response
	if err := c.model.post(ctx, "/responses", body, &resp); err != nil {
		return Reply{}, err
	}
	c.model.add(resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.InputTokensDetails.CachedTokens)
	if resp.Status == "failed" {
		return Reply{}, errors.New(c.model.provider.Name + ": the response failed")
	}

	reply := Reply{Stop: StopEndTurn}
	if resp.Status == "incomplete" && resp.IncompleteDetails != nil {
		switch resp.IncompleteDetails.Reason {
		case "max_output_tokens":
			reply.Stop = StopMaxTokens
		case "content_filter":
			reply.Stop, reply.Refusal = StopRefusal, "content_filter"
		}
	}
	var texts []string
	for _, raw := range resp.Output {
		var item outputItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return Reply{}, fmt.Errorf("%s: reading the output: %w", c.model.provider.Name, err)
		}
		// Every item goes back as it came, the reasoning included.
		c.input = append(c.input, raw)
		switch item.Type {
		case "message":
			for _, p := range item.Content {
				switch p.Type {
				case "output_text":
					texts = append(texts, p.Text)
				case "refusal":
					reply.Stop, reply.Refusal = StopRefusal, p.Refusal
				}
			}
		case "function_call":
			input := json.RawMessage(item.Arguments)
			if strings.TrimSpace(item.Arguments) == "" {
				input = json.RawMessage("{}")
			}
			call := Call{ID: item.CallID, Name: item.Name, Input: input}
			if c.browser[call.Name] {
				call.Toolset = ToolsetBrowser
			}
			reply.Calls = append(reply.Calls, call)
		}
	}
	reply.Text = strings.TrimSpace(strings.Join(texts, "\n"))
	if len(reply.Calls) > 0 && reply.Stop == StopEndTurn {
		reply.Stop = StopToolUse
	}
	return reply, nil
}

// turnItems are the items of a turn: the output of each call, its images
// included, then the text of the turn as a message of the user.
func (c *responsesChat) turnItems(turn Turn) []any {
	var out []any
	for _, r := range turn.Results {
		text := r.Text
		if len(r.Tabs) > 0 && !r.IsError {
			for _, t := range r.Tabs {
				text = strings.TrimSpace(text + fmt.Sprintf("\nThe tab now shows %q at %s.", t.Title, t.URL))
			}
		}
		item := map[string]any{"type": "function_call_output", "call_id": r.CallID}
		if len(r.Images) > 0 && c.model.vision {
			item["output"] = c.model.responsesContent(text, r.Images)
		} else {
			if text == "" {
				text = "Done."
			}
			item["output"] = text
		}
		out = append(out, item)
	}
	if turn.Text != "" {
		out = append(out, c.model.responsesUser(turn.Text, nil))
	}
	return out
}
