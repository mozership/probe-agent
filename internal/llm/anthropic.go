package llm

import (
	"context"
	"encoding/base64"
	"slices"
	"strings"
	"sync"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
)

// DefaultModel is the model the agent uses unless told another.
const DefaultModel = "claude-opus-5-5"

// maxTokens bounds each reply. A reply of the agent is a few tool calls, so
// this is far more than one needs, and still short enough to be sent
// without streaming.
const maxTokens = 16000

// fallbackModels are the models that take the server-side fallback, by
// which a request the model declines is answered by another model rather
// than stopped.
var fallbackModels = []string{"claude-fable-5-1", "claude-opus-5-5", "claude-opus-5", "claude-sonnet-5-5"}

// updatesModels are the models that report their progress between tool
// calls as notes.
var updatesModels = []string{"claude-fable-5-1", "claude-fable-5", "claude-opus-5-5", "claude-sonnet-5-5"}

// Anthropic is a Claude model reached through the Claude API. The client
// finds its credentials as the Anthropic SDK does, such as in
// ANTHROPIC_API_KEY.
type Anthropic struct {
	client anthropic.Client
	model  string
	effort string

	mu    sync.Mutex
	usage Usage
}

// NewAnthropic returns the model of the ID given, which thinks with the
// effort given, such as medium; an empty effort leaves it to the model.
func NewAnthropic(model, effort string, opts ...option.RequestOption) *Anthropic {
	return &Anthropic{client: anthropic.NewClient(opts...), model: model, effort: effort}
}

// Name returns the model's ID.
func (a *Anthropic) Name() string { return a.model }

// Usage returns what every chat of the model has used so far.
func (a *Anthropic) Usage() Usage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.usage
}

func (a *Anthropic) add(u anthropic.BetaUsage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.usage = a.usage.Add(Usage{
		Requests:         1,
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
	})
}

// Chat starts a chat with the model.
func (a *Anthropic) Chat(opts ChatOptions) Chat {
	return &anthropicChat{model: a, params: a.params(opts)}
}

// params are the parameters of the first request of a chat. Later requests
// only add to Messages, so that the requests share a prefix the API caches.
func (a *Anthropic) params(opts ChatOptions) anthropic.BetaMessageNewParams {
	content := []anthropic.BetaContentBlockParamUnion{anthropic.NewBetaTextBlock(opts.Prompt)}
	for _, img := range opts.Images {
		content = append(content, imageBlock(img))
	}
	p := anthropic.BetaMessageNewParams{
		Model:     a.model,
		MaxTokens: maxTokens,
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(content...)},
		// The conversation grows by a turn at a time, so the cache is kept
		// up to its end on every request.
		CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
	}
	if opts.System != "" {
		p.System = []anthropic.BetaTextBlockParam{{Text: opts.System}}
	}
	if a.effort != "" {
		p.OutputConfig.Effort = anthropic.BetaOutputConfigEffort(a.effort)
	}
	if slices.Contains(fallbackModels, a.model) {
		p.Fallbacks = anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
		p.Betas = append(p.Betas, anthropic.AnthropicBetaServerSideFallback2026_07_01)
	}
	if slices.Contains(updatesModels, a.model) {
		p.Thinking = anthropic.BetaThinkingConfigParamUnion{OfAdaptive: &anthropic.BetaThinkingConfigAdaptiveParam{
			Display: anthropic.BetaThinkingConfigAdaptiveDisplayUpdates,
		}}
		p.Betas = append(p.Betas, anthropic.AnthropicBetaThinkingDisplayUpdates2026_08_18)
	}
	if opts.Browser != nil {
		p.Tools = append(p.Tools, anthropic.BetaToolUnionParam{OfBrowserToolset20260801: &anthropic.BetaBrowserToolset20260801Param{
			Configs: browserConfigs(opts.Browser.Disabled),
		}})
	}
	for _, t := range opts.Tools {
		p.Tools = append(p.Tools, anthropic.BetaToolUnionParam{OfTool: &anthropic.BetaToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: anthropic.BetaToolInputSchemaParam{Properties: t.Properties, Required: t.Required},
		}})
	}
	return p
}

type anthropicChat struct {
	model   *Anthropic
	params  anthropic.BetaMessageNewParams
	started bool
	// toolsets are the toolsets of the calls of the last reply, by ID, as
	// the results of the calls name them.
	toolsets map[string]string
}

// Send sends the prompt on the first call, and turn on each call after.
func (c *anthropicChat) Send(ctx context.Context, turn Turn) (Reply, error) {
	if c.started {
		c.params.Messages = append(c.params.Messages, anthropic.NewBetaUserMessage(c.turnBlocks(turn)...))
	}
	c.started = true

	resp, err := c.model.client.Beta.Messages.New(ctx, c.params)
	if err != nil {
		return Reply{}, err
	}
	c.model.add(resp.Usage)
	// The reply goes back as it came, thinking blocks included, which the
	// API needs to see again unchanged.
	c.params.Messages = append(c.params.Messages, resp.ToParam())
	reply := replyOf(resp)
	c.toolsets = map[string]string{}
	for _, call := range reply.Calls {
		c.toolsets[call.ID] = call.Toolset
	}
	return reply, nil
}

func replyOf(resp *anthropic.BetaMessage) Reply {
	r := Reply{Stop: StopReason(resp.StopReason)}
	var texts []string
	for _, block := range resp.Content {
		switch b := block.AsAny().(type) {
		case anthropic.BetaTextBlock:
			texts = append(texts, b.Text)
		case anthropic.BetaThinkingBlock:
			if s := strings.TrimSpace(b.Thinking); s != "" {
				r.Notes = append(r.Notes, s)
			}
		case anthropic.BetaToolUseBlock:
			r.Calls = append(r.Calls, Call{
				ID:      b.ID,
				Name:    b.Name,
				Toolset: b.ToolsetName,
				Input:   []byte(b.JSON.Input.Raw()),
			})
		}
	}
	r.Text = strings.TrimSpace(strings.Join(texts, "\n"))
	if r.Stop == StopRefusal {
		r.Refusal = strings.TrimSpace(string(resp.StopDetails.Category) + ": " + resp.StopDetails.Explanation)
	}
	return r
}

func (c *anthropicChat) turnBlocks(turn Turn) []anthropic.BetaContentBlockParamUnion {
	var blocks []anthropic.BetaContentBlockParamUnion
	for _, r := range turn.Results {
		blocks = append(blocks, resultBlock(r, c.toolsets[r.CallID]))
	}
	if turn.Text != "" {
		blocks = append(blocks, anthropic.NewBetaTextBlock(turn.Text))
	}
	return blocks
}

// resultBlock is the tool_result block of r, a result of a call of
// toolset. A result of the browser toolset names the toolset, and reports
// the tabs in a browser_state block, which the API renders for the model.
func resultBlock(r Result, toolset string) anthropic.BetaContentBlockParamUnion {
	b := anthropic.BetaToolResultBlockParam{ToolUseID: r.CallID}
	if toolset != "" {
		b.ToolsetName = anthropic.String(toolset)
	}
	if r.IsError {
		b.IsError = anthropic.Bool(true)
	}
	if r.Text != "" {
		b.Content = append(b.Content, anthropic.BetaToolResultBlockParamContentUnion{OfText: &anthropic.BetaTextBlockParam{Text: r.Text}})
	}
	for _, img := range r.Images {
		b.Content = append(b.Content, anthropic.BetaToolResultBlockParamContentUnion{OfImage: &anthropic.BetaImageBlockParam{
			Source: anthropic.BetaImageBlockParamSourceUnion{OfBase64: &anthropic.BetaBase64ImageSourceParam{
				Data:      base64.StdEncoding.EncodeToString(img.Data),
				MediaType: anthropic.BetaBase64ImageSourceMediaType(img.MediaType),
			}},
		}})
	}
	if len(r.Tabs) > 0 && !r.IsError {
		state := anthropic.BetaBrowserStateBlockParam{}
		for _, t := range r.Tabs {
			e := anthropic.BetaBrowserStateTabEntryParam{TabID: t.ID, Title: t.Title, URL: t.URL}
			if t.Active {
				e.Active = anthropic.Bool(true)
			}
			state.Tabs = append(state.Tabs, e)
		}
		b.Content = append(b.Content, anthropic.BetaToolResultBlockParamContentUnion{OfBrowserState: &state})
	}
	return anthropic.BetaContentBlockParamUnion{OfToolResult: &b}
}

func imageBlock(img Image) anthropic.BetaContentBlockParamUnion {
	return anthropic.BetaContentBlockParamUnion{OfImage: &anthropic.BetaImageBlockParam{
		Source: anthropic.BetaImageBlockParamSourceUnion{OfBase64: &anthropic.BetaBase64ImageSourceParam{
			Data:      base64.StdEncoding.EncodeToString(img.Data),
			MediaType: anthropic.BetaBase64ImageSourceMediaType(img.MediaType),
		}},
	}}
}

// browserConfigs turns off the members of the browser toolset named in
// disabled. A name that is not a member is left out.
func browserConfigs(disabled []string) anthropic.BetaBrowserToolsetConfigsParam {
	var c anthropic.BetaBrowserToolsetConfigsParam
	off := param.NewOpt(false)
	for _, name := range disabled {
		switch name {
		case "close_tab":
			c.CloseTab.Enabled = off
		case "double_click":
			c.DoubleClick.Enabled = off
		case "file_upload":
			c.FileUpload.Enabled = off
		case "find":
			c.Find.Enabled = off
		case "form_input":
			c.FormInput.Enabled = off
		case "get_page_text":
			c.GetPageText.Enabled = off
		case "hold_key":
			c.HoldKey.Enabled = off
		case "hover":
			c.Hover.Enabled = off
		case "javascript_exec":
			c.JavascriptExec.Enabled = off
		case "key":
			c.Key.Enabled = off
		case "left_click":
			c.LeftClick.Enabled = off
		case "left_click_drag":
			c.LeftClickDrag.Enabled = off
		case "left_mouse_down":
			c.LeftMouseDown.Enabled = off
		case "left_mouse_up":
			c.LeftMouseUp.Enabled = off
		case "list_tabs":
			c.ListTabs.Enabled = off
		case "middle_click":
			c.MiddleClick.Enabled = off
		case "mouse_move":
			c.MouseMove.Enabled = off
		case "navigate":
			c.Navigate.Enabled = off
		case "new_tab":
			c.NewTab.Enabled = off
		case "read_console":
			c.ReadConsole.Enabled = off
		case "read_network":
			c.ReadNetwork.Enabled = off
		case "read_page":
			c.ReadPage.Enabled = off
		case "right_click":
			c.RightClick.Enabled = off
		case "screenshot":
			c.Screenshot.Enabled = off
		case "scroll":
			c.Scroll.Enabled = off
		case "scroll_to":
			c.ScrollTo.Enabled = off
		case "switch_tab":
			c.SwitchTab.Enabled = off
		case "triple_click":
			c.TripleClick.Enabled = off
		case "type":
			c.Type.Enabled = off
		case "wait":
			c.Wait.Enabled = off
		case "zoom":
			c.Zoom.Enabled = off
		}
	}
	return c
}
