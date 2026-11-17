package scenario

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mozership/probe-agent/internal/llm"
)

// IOS is an app on an iOS simulator, which the tester drives through
// probe-ios.
type IOS struct {
	// App is the bundle identifier of the app.
	App string
	// Device is the simulator, by its name or UDID; empty for the one
	// that is booted.
	Device string
	// Call runs probe-ios once with actions, the entries of its with.actions,
	// for App on Device, and returns its res. An action that cannot finish,
	// such as a tap on an element that is not there, is an error.
	Call func(ctx context.Context, actions []map[string]any) (map[string]any, error)
}

// iosDriver is an iOS app as a target. Each tool is one call of probe-ios,
// and a move returns the accessibility tree after it, as a move in the
// browser returns the page.
type iosDriver struct {
	*ledger
	app    IOS
	vision bool
	// device is the UDID of the simulator, as probe-ios last said it.
	device string
}

var _ Driver = (*iosDriver)(nil)

func newIOSDriver(l *ledger, app IOS, vision bool) *iosDriver {
	return &iosDriver{ledger: l, app: app, vision: vision}
}

// Kind is ios.
func (d *iosDriver) Kind() string { return "ios" }

// Open brings the app to the foreground, launching it if it is not
// running, and reads its tree. probe-ios builds its runner on its first
// call, which takes minutes, so this is where a test waits for it.
func (d *iosDriver) Open(ctx context.Context) error {
	res, err := d.call(ctx, []map[string]any{{"name": "activate"}, {"name": "tree"}})
	if err != nil {
		return fmt.Errorf("opening the app %s: %w", d.app.App, err)
	}
	d.seen(results(res)["tree"])
	return nil
}

// Offer offers the tools of the app.
func (d *iosDriver) Offer(opts *llm.ChatOptions) {
	opts.Tools = append(opts.Tools, iosTools(d.vision)...)
}

// Handles says whether call is of a tool of the app the model was
// offered: not ios_screenshot for a model that sees no images.
func (d *iosDriver) Handles(call llm.Call) bool {
	if call.Toolset != "" {
		return false
	}
	for _, t := range iosTools(d.vision) {
		if t.Name == call.Name {
			return true
		}
	}
	return false
}

// Brief tells the tester of the app and of how to name its elements.
func (d *iosDriver) Brief() string {
	return `The iOS app ` + d.describe() + `, through the tools that start with ios_:
- Read the screen with ios_tree before you act on it. A tap, a type, a launch and an activate return the tree after them.
- Name an element by a selector: key=value pairs apart by spaces, a value with a space in double quotes. The keys are identifier (or id), the accessibility identifier; label, mostly the text shown; type, the element type, such as button, cell, text, textfield, securetextfield, switch, navigationbar or any; and index, which of the matching elements, from 0. For example: type=button label="Sign in".
- A selector that matches nothing fails after a wait. Read the tree again, and name the element by what it shows.`
}

// Opening says which app is open.
func (d *iosDriver) Opening() string {
	return "The app " + d.describe() + " is open."
}

func (d *iosDriver) describe() string {
	if d.app.Device == "" {
		return d.app.App + " on the booted simulator"
	}
	return d.app.App + " on " + d.app.Device
}

// iosInput is the input of any tool of the app; each tool reads the
// fields it takes.
type iosInput struct {
	Selector string   `json:"selector"`
	Value    string   `json:"value"`
	Args     []string `json:"args"`
}

// Do runs a call of a tool of the app.
func (d *iosDriver) Do(ctx context.Context, call llm.Call) (llm.Result, Step) {
	step := Step{Tool: call.Name, Target: d.Kind()}
	var raw map[string]any
	if json.Unmarshal(call.Input, &raw) == nil && len(raw) > 0 {
		step.Input, _ = d.secrets.maskAny(raw).(map[string]any)
	}
	res := llm.Result{CallID: call.ID}
	var in iosInput
	text, images, shot, err := "", []llm.Image(nil), []byte(nil), json.Unmarshal(call.Input, &in)
	if err == nil {
		text, images, shot, err = d.tool(ctx, call.Name, in)
	}
	if err != nil {
		// What the model reads is masked, as an error can quote what was
		// typed.
		res.Text = "Error: " + d.secrets.mask(err.Error())
		res.IsError = true
		step.Error = res.Text
	} else {
		res.Text = d.secrets.mask(text)
		res.Images = images
		step.Result = summary(res.Text)
	}
	if shot != nil {
		step.Screenshot = d.save(fmt.Sprintf("step-%02d.png", len(d.steps)+1), shot)
	}
	return res, step
}

// moves are the tools that change the screen, after which the tree is
// read, and a screenshot kept when the model sees images.
var moves = map[string]bool{
	"ios_tap": true, "ios_type": true, "ios_launch": true, "ios_activate": true, "ios_terminate": true,
}

// tool runs the tool name with in, and returns the text and the images of
// its result, and the screenshot to keep as evidence of a move.
func (d *iosDriver) tool(ctx context.Context, name string, in iosInput) (string, []llm.Image, []byte, error) {
	var actions []map[string]any
	var did string
	needSelector := func() error {
		if strings.TrimSpace(in.Selector) == "" {
			return fmt.Errorf("%s needs a selector", name)
		}
		return nil
	}
	switch name {
	case "ios_tree":
		actions = []map[string]any{{"name": "tree"}}
	case "ios_screenshot":
		actions = []map[string]any{{"name": "screenshot"}}
	case "ios_tap":
		if err := needSelector(); err != nil {
			return "", nil, nil, err
		}
		actions = []map[string]any{{"name": "tap", "selector": in.Selector}}
		did = "Tapped " + in.Selector + "."
	case "ios_type":
		if err := needSelector(); err != nil {
			return "", nil, nil, err
		}
		value, err := d.secrets.fill(in.Value)
		if err != nil {
			return "", nil, nil, err
		}
		actions = []map[string]any{{"name": "type", "selector": in.Selector, "value": value}}
		did = "Typed " + quote(in.Value) + " into " + in.Selector + "."
	case "ios_wait":
		if err := needSelector(); err != nil {
			return "", nil, nil, err
		}
		actions = []map[string]any{{"name": "wait", "selector": in.Selector}}
		did = in.Selector + " is on the screen."
	case "ios_text":
		if err := needSelector(); err != nil {
			return "", nil, nil, err
		}
		actions = []map[string]any{{"name": "text", "selector": in.Selector}}
	case "ios_launch":
		launch := map[string]any{"name": "launch"}
		if len(in.Args) > 0 {
			launch["args"] = in.Args
		}
		actions = []map[string]any{launch}
		did = "Launched the app afresh."
	case "ios_activate":
		actions = []map[string]any{{"name": "activate"}}
		did = "Brought the app to the foreground."
	case "ios_terminate":
		actions = []map[string]any{{"name": "terminate"}}
		did = "Ended the app."
	default:
		return "", nil, nil, fmt.Errorf("there is no tool %s", name)
	}
	if moves[name] {
		// An app that was ended has no tree to read.
		if name != "ios_terminate" {
			actions = append(actions, map[string]any{"name": "tree"})
		}
		if d.vision {
			actions = append(actions, map[string]any{"name": "screenshot"})
		}
	}

	res, err := d.call(ctx, actions)
	if err != nil {
		return "", nil, nil, err
	}
	got := results(res)
	switch name {
	case "ios_tree":
		d.seen(got["tree"])
		return truncate(got["tree"], maxText), nil, nil, nil
	case "ios_screenshot":
		img, err := screenshotOf(res)
		if err != nil {
			return "", nil, nil, err
		}
		return "", []llm.Image{{MediaType: "image/png", Data: img}}, nil, nil
	case "ios_text":
		d.seen(got["text"])
		if got["text"] == "" {
			return "The element shows no text.", nil, nil, nil
		}
		return got["text"], nil, nil, nil
	case "ios_wait":
		return did, nil, nil, nil
	}
	var shot []byte
	if d.vision {
		shot, _ = screenshotOf(res)
	}
	tree, ok := got["tree"]
	if !ok {
		return did, nil, shot, nil
	}
	d.seen(tree)
	return did + " The screen now:\n" + truncate(tree, maxText), nil, shot, nil
}

// Final is the screen the tester left the app on.
func (d *iosDriver) Final(ctx context.Context, vision bool) observation {
	o := observation{
		heading:   "The screen the tester left the app " + d.describe() + " on:",
		mediaType: "image/png",
		imageNote: "A screenshot of the final screen of the app follows.",
	}
	var tree string
	if res, err := d.call(ctx, []map[string]any{{"name": "tree"}, {"name": "screenshot"}}); err == nil {
		tree = truncate(d.secrets.mask(results(res)["tree"]), 20000)
		if img, err := screenshotOf(res); err == nil {
			// The judge is shown the screenshot when the model sees images;
			// it is kept as evidence either way.
			if vision {
				o.image = img
			}
			o.screenshot = d.save("final-ios.png", img)
		}
	}
	o.sections = []section{{"final_app_tree", tree}}
	o.state = TargetState{Kind: d.Kind(), App: d.app.App, Device: d.device}
	if o.state.Device == "" {
		o.state.Device = d.app.Device
	}
	return o
}

// call runs probe-ios with actions, and keeps the simulator it says it ran
// them on.
func (d *iosDriver) call(ctx context.Context, actions []map[string]any) (map[string]any, error) {
	res, err := d.app.Call(ctx, actions)
	if err != nil {
		return nil, err
	}
	if dev, ok := res["device"].(string); ok && dev != "" {
		d.device = dev
	}
	return res, nil
}

// results are what text and tree read, by their keys, which are the names
// of the actions.
func results(res map[string]any) map[string]string {
	out := map[string]string{}
	m, _ := res["results"].(map[string]any)
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// screenshotOf reads the PNG that screenshot saved.
func screenshotOf(res map[string]any) ([]byte, error) {
	paths, _ := res["filepaths"].(map[string]any)
	path, _ := paths["screenshot"].(string)
	if path == "" {
		return nil, errors.New("probe-ios saved no screenshot")
	}
	return os.ReadFile(path)
}

var selectorProperty = map[string]any{
	"type":        "string",
	"description": `The element, as key=value pairs apart by spaces: identifier, label, type and index, such as type=button label="Sign in". A bare word is an identifier.`,
}

// iosTools are the tools of the app, but for ios_screenshot when the model
// sees no images.
func iosTools(vision bool) []llm.Tool {
	tools := []llm.Tool{
		{
			Name:        "ios_tree",
			Description: "Read the accessibility tree of the app: each element with its type, identifier, label and value, which a selector names it by.",
			Properties:  map[string]any{},
		},
		{
			Name:        "ios_tap",
			Description: "Tap an element of the app. Returns the tree after it.",
			Properties:  map[string]any{"selector": selectorProperty},
			Required:    []string{"selector"},
		},
		{
			Name:        "ios_type",
			Description: "Tap a field of the app and type text into it. Returns the tree after it.",
			Properties: map[string]any{
				"selector": selectorProperty,
				"value":    map[string]any{"type": "string", "description": "The text to type."},
			},
			Required: []string{"selector", "value"},
		},
		{
			Name:        "ios_wait",
			Description: "Wait until an element is on the screen, as after a screen that is slow to load.",
			Properties:  map[string]any{"selector": selectorProperty},
			Required:    []string{"selector"},
		},
		{
			Name:        "ios_text",
			Description: "Read the label of an element, or its value when it has no label.",
			Properties:  map[string]any{"selector": selectorProperty},
			Required:    []string{"selector"},
		},
		{
			Name:        "ios_launch",
			Description: "Launch the app afresh, ending it first if it runs. Returns the tree after it.",
			Properties: map[string]any{
				"args": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Launch arguments, such as [\"-AppleLanguages\", \"(en)\"]."},
			},
		},
		{
			Name:        "ios_activate",
			Description: "Bring the app to the foreground, launching it if it is not running. Returns the tree after it.",
			Properties:  map[string]any{},
		},
		{
			Name:        "ios_terminate",
			Description: "End the app.",
			Properties:  map[string]any{},
		},
	}
	if vision {
		tools = append(tools, llm.Tool{
			Name:        "ios_screenshot",
			Description: "See the screen of the app as an image.",
			Properties:  map[string]any{},
		})
	}
	return tools
}
