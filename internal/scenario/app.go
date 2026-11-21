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

// App is an app on a device: on an iOS simulator, which the tester drives
// through probe-ios, or on an Android emulator or device, which it drives
// through probe-android. Both actions take the same with, and answer in
// the same res.
type App struct {
	// App is the bundle identifier of an iOS app, or the package name of
	// an Android one.
	App string
	// Device is the simulator or the device, by its name, UDID or serial;
	// empty for the one there is.
	Device string
	// Call runs the action once with actions, the entries of its
	// with.actions, for App on Device, and returns its res. An action that
	// cannot finish, such as a tap on an element that is not there, is an
	// error.
	Call func(ctx context.Context, actions []map[string]any) (map[string]any, error)
}

// IOS is an app on an iOS simulator.
type IOS = App

// platform is what an app driver knows of the system of its device.
type platform struct {
	// kind is the kind of the target, and the start of the names of its
	// tools: ios or android.
	kind string
	// name names the system, as iOS.
	name string
	// selectors tells the tester how to name an element, and selector is
	// the description of a selector in a tool.
	selectors string
	selector  string
	// anyDevice describes the device when none is named.
	anyDevice string
	// launchArgs says whether launch takes arguments, and back whether
	// there is a back button to press.
	launchArgs bool
	back       bool
}

var iosPlatform = platform{
	kind:       "ios",
	name:       "iOS",
	selectors:  `Name an element by a selector: key=value pairs apart by spaces, a value with a space in double quotes. The keys are identifier (or id), the accessibility identifier; label, mostly the text shown; type, the element type, such as button, cell, text, textfield, securetextfield, switch, navigationbar or any; and index, which of the matching elements, from 0. For example: type=button label="Sign in".`,
	selector:   `The element, as key=value pairs apart by spaces: identifier, label, type and index, such as type=button label="Sign in". A bare word is an identifier.`,
	anyDevice:  "the booted simulator",
	launchArgs: true,
}

var androidPlatform = platform{
	kind:      "android",
	name:      "Android",
	selectors: `Name an element by a selector: key=value pairs apart by spaces, a value with a space in double quotes. The keys are id, the resource id, whole as com.example:id/login or by its name as login; text, the text shown; desc, the content description, as of an icon; class, whole as android.widget.Button or by its name as Button or EditText; and index, which of the matching elements, from 0. For example: class=Button text="Sign in".`,
	selector:  `The element, as key=value pairs apart by spaces: id, text, desc, class and index, such as class=Button text="Sign in". A bare word is an id.`,
	anyDevice: "the one device",
	back:      true,
}

// appDriver is an app on a device as a target. Each tool is one call of
// the action of its platform, and a move returns the tree of the screen
// after it, as a move in the browser returns the page.
type appDriver struct {
	*ledger
	p      platform
	app    App
	vision bool
	// device is the device, as the action last said it.
	device string
	// looked is whether the tester saw a screenshot of the screen since
	// the last move, and pixels whether it had seen one before the last
	// move: a value it typed then may have been read off the image.
	looked, pixels bool
}

var _ Driver = (*appDriver)(nil)

func newAppDriver(l *ledger, p platform, app App, vision bool) *appDriver {
	return &appDriver{ledger: l, p: p, app: app, vision: vision}
}

// Kind is ios or android.
func (d *appDriver) Kind() string { return d.p.kind }

// Open brings the app to the foreground, launching it if it is not
// running, and reads its tree. probe-ios builds its runner on its first
// call, which takes minutes, so this is where a test of an iOS app waits
// for it.
func (d *appDriver) Open(ctx context.Context) error {
	res, err := d.call(ctx, []map[string]any{{"name": "activate"}, {"name": "tree"}})
	if err != nil {
		return fmt.Errorf("opening the app %s: %w", d.app.App, err)
	}
	d.seen(results(res)["tree"])
	return nil
}

// Offer offers the tools of the app.
func (d *appDriver) Offer(opts *llm.ChatOptions) {
	opts.Tools = append(opts.Tools, appTools(d.p, d.vision)...)
}

// Handles says whether call is of a tool of the app the model was
// offered: not the screenshot for a model that sees no images.
func (d *appDriver) Handles(call llm.Call) bool {
	if call.Toolset != "" {
		return false
	}
	for _, t := range appTools(d.p, d.vision) {
		if t.Name == call.Name {
			return true
		}
	}
	return false
}

// Brief tells the tester of the app and of how to name its elements.
func (d *appDriver) Brief() string {
	k := d.p.kind
	moves := "A tap, a type, a launch and an activate return the tree after them."
	if d.p.back {
		moves = "A tap, a type, a back, a launch and an activate return the tree after them; " + k + "_back presses the back button."
	}
	return `The ` + d.p.name + ` app ` + d.describe() + `, through the tools that start with ` + k + `_:
- Read the screen with ` + k + `_tree before you act on it. ` + moves + `
- ` + d.p.selectors + `
- A selector that matches nothing fails after a wait. Read the tree again, and name the element by what it shows.`
}

// Opening says which app is open.
func (d *appDriver) Opening() string {
	return "The app " + d.describe() + " is open."
}

func (d *appDriver) describe() string {
	if d.app.Device == "" {
		return d.app.App + " on " + d.p.anyDevice
	}
	return d.app.App + " on " + d.app.Device
}

// appInput is the input of any tool of the app; each tool reads the
// fields it takes.
type appInput struct {
	Selector string   `json:"selector"`
	Value    string   `json:"value"`
	Args     []string `json:"args"`
}

// Do runs a call of a tool of the app.
func (d *appDriver) Do(ctx context.Context, call llm.Call) (llm.Result, Step) {
	step := Step{Tool: call.Name, Target: d.Kind()}
	var raw map[string]any
	if json.Unmarshal(call.Input, &raw) == nil && len(raw) > 0 {
		step.Input, _ = d.secrets.maskAny(raw).(map[string]any)
	}
	res := llm.Result{CallID: call.ID}
	var in appInput
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

// moves are the actions that change the screen, after whose tools the tree
// is read, and a screenshot kept when the model sees images.
var moves = map[string]bool{
	"tap": true, "type": true, "back": true, "launch": true, "activate": true, "terminate": true,
}

// tool runs the tool name with in, and returns the text and the images of
// its result, and the screenshot to keep as evidence of a move.
func (d *appDriver) tool(ctx context.Context, tool string, in appInput) (string, []llm.Image, []byte, error) {
	// The tools are named by the kind of the target and the action of the
	// platform, as ios_tap.
	name, ok := strings.CutPrefix(tool, d.p.kind+"_")
	if !ok || (name == "back" && !d.p.back) {
		return "", nil, nil, fmt.Errorf("there is no tool %s", tool)
	}
	var actions []map[string]any
	var did string
	needSelector := func() error {
		if strings.TrimSpace(in.Selector) == "" {
			return fmt.Errorf("%s needs a selector", tool)
		}
		return nil
	}
	switch name {
	case "tree":
		actions = []map[string]any{{"name": "tree"}}
	case "screenshot":
		actions = []map[string]any{{"name": "screenshot"}}
	case "tap":
		if err := needSelector(); err != nil {
			return "", nil, nil, err
		}
		actions = []map[string]any{{"name": "tap", "selector": in.Selector}}
		did = "Tapped " + in.Selector + "."
	case "type":
		if err := needSelector(); err != nil {
			return "", nil, nil, err
		}
		value, err := d.secrets.fill(in.Value)
		if err != nil {
			return "", nil, nil, err
		}
		actions = []map[string]any{{"name": "type", "selector": in.Selector, "value": value}}
		did = "Typed " + quote(in.Value) + " into " + in.Selector + "."
	case "wait":
		if err := needSelector(); err != nil {
			return "", nil, nil, err
		}
		actions = []map[string]any{{"name": "wait", "selector": in.Selector}}
		did = in.Selector + " is on the screen."
	case "text":
		if err := needSelector(); err != nil {
			return "", nil, nil, err
		}
		actions = []map[string]any{{"name": "text", "selector": in.Selector}}
	case "launch":
		launch := map[string]any{"name": "launch"}
		if len(in.Args) > 0 && d.p.launchArgs {
			launch["args"] = in.Args
		}
		actions = []map[string]any{launch}
		did = "Launched the app afresh."
	case "activate":
		actions = []map[string]any{{"name": "activate"}}
		did = "Brought the app to the foreground."
	case "terminate":
		actions = []map[string]any{{"name": "terminate"}}
		did = "Ended the app."
	case "back":
		actions = []map[string]any{{"name": "back"}}
		did = "Pressed the back button."
	default:
		return "", nil, nil, fmt.Errorf("there is no tool %s", tool)
	}
	if moves[name] {
		d.pixels, d.looked = d.looked, false
		// An app that was ended has no tree to read.
		if name != "terminate" {
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
	case "tree":
		d.seen(got["tree"])
		return truncate(got["tree"], maxText), nil, nil, nil
	case "screenshot":
		img, err := screenshotOf(res)
		if err != nil {
			return "", nil, nil, err
		}
		d.looked = true
		return "", []llm.Image{{MediaType: "image/png", Data: img}}, nil, nil
	case "text":
		d.seen(got["text"])
		if got["text"] == "" {
			return "The element shows no text.", nil, nil, nil
		}
		return got["text"], nil, nil, nil
	case "wait":
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
func (d *appDriver) Final(ctx context.Context, vision bool) observation {
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
			o.screenshot = d.save("final-"+d.p.kind+".png", img)
		}
	}
	o.sections = []section{{"final_" + d.p.kind + "_tree", tree}}
	o.state = TargetState{Kind: d.Kind(), App: d.app.App, Device: d.device}
	if o.state.Device == "" {
		o.state.Device = d.app.Device
	}
	return o
}

// call runs the action with actions, and keeps the device it says it ran
// them on.
func (d *appDriver) call(ctx context.Context, actions []map[string]any) (map[string]any, error) {
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
		return nil, errors.New("the action saved no screenshot")
	}
	return os.ReadFile(path)
}

// appTools are the tools of the app of p, but for the screenshot when the
// model sees no images.
func appTools(p platform, vision bool) []llm.Tool {
	k := p.kind
	selector := map[string]any{"type": "string", "description": p.selector}
	launch := llm.Tool{
		Name:        k + "_launch",
		Description: "Launch the app afresh, ending it first if it runs. Returns the tree after it.",
		Properties:  map[string]any{},
	}
	if p.launchArgs {
		launch.Properties = map[string]any{
			"args": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Launch arguments, such as [\"-AppleLanguages\", \"(en)\"]."},
		}
	}
	tools := []llm.Tool{
		{
			Name:        k + "_tree",
			Description: "Read the tree of the screen of the app: each element with what a selector names it by.",
			Properties:  map[string]any{},
		},
		{
			Name:        k + "_tap",
			Description: "Tap an element of the app. Returns the tree after it.",
			Properties:  map[string]any{"selector": selector},
			Required:    []string{"selector"},
		},
		{
			Name:        k + "_type",
			Description: "Tap a field of the app and type text into it. Returns the tree after it.",
			Properties: map[string]any{
				"selector": selector,
				"value":    map[string]any{"type": "string", "description": "The text to type."},
			},
			Required: []string{"selector", "value"},
		},
		{
			Name:        k + "_wait",
			Description: "Wait until an element is on the screen, as after a screen that is slow to load.",
			Properties:  map[string]any{"selector": selector},
			Required:    []string{"selector"},
		},
		{
			Name:        k + "_text",
			Description: "Read the text of an element, or what describes it when it shows none.",
			Properties:  map[string]any{"selector": selector},
			Required:    []string{"selector"},
		},
		launch,
		{
			Name:        k + "_activate",
			Description: "Bring the app to the foreground, launching it if it is not running. Returns the tree after it.",
			Properties:  map[string]any{},
		},
		{
			Name:        k + "_terminate",
			Description: "End the app.",
			Properties:  map[string]any{},
		},
	}
	if p.back {
		tools = append(tools, llm.Tool{
			Name:        k + "_back",
			Description: "Press the back button. Returns the tree after it.",
			Properties:  map[string]any{},
		})
	}
	if vision {
		tools = append(tools, llm.Tool{
			Name:        k + "_screenshot",
			Description: "See the screen of the app as an image.",
			Properties:  map[string]any{},
		})
	}
	return tools
}
