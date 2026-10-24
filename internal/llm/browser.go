package llm

import (
	"slices"
	"strings"
)

// The browser toolset is a tool of the Claude API, which Claude is trained
// to use. Another provider has no such tool, so a model of it is offered
// its members as tools of the agent's own, with the inputs the toolset
// takes, so that the agent runs the calls of either alike. The members are
// those the agent runs; the others it disables for every model.

// target is the schema of the element or the point a member acts on.
func target(vision bool, refOnly bool) map[string]any {
	if refOnly || !vision {
		return map[string]any{
			"type":        "object",
			"description": "The element, by the ref read_page or find gave it, such as ref_3.",
			"properties": map[string]any{
				"type": map[string]any{"type": "string", "enum": []string{"ref"}},
				"ref":  map[string]any{"type": "string", "description": "The ref of the element, such as ref_3."},
			},
			"required": []string{"type", "ref"},
		}
	}
	return map[string]any{
		"type":        "object",
		"description": "The element, by the ref read_page or find gave it, or a point of the viewport, in CSS pixels from its top left corner, as a screenshot shows it. Prefer a ref.",
		"properties": map[string]any{
			"type": map[string]any{"type": "string", "enum": []string{"ref", "coordinate"}},
			"ref":  map[string]any{"type": "string", "description": "The ref of the element, such as ref_3, for a ref target."},
			"x":    map[string]any{"type": "number", "description": "The x of the point, for a coordinate target."},
			"y":    map[string]any{"type": "number", "description": "The y of the point, for a coordinate target."},
		},
		"required": []string{"type"},
	}
}

var modifiers = map[string]any{
	"type":        "string",
	"description": "Keys to hold down during the click, joined by +, such as ctrl+shift.",
}

// browserTools returns the members of the browser toolset as tools, but
// for those disabled, and those that show the page as an image when the
// model sees no images.
func browserTools(disabled []string, vision bool) []Tool {
	// what is said of an element or a point, which only a model that
	// sees the page can name.
	what := func(s string) string {
		if !vision {
			return strings.Replace(s, " or a point", "", 1)
		}
		return s
	}
	click := func(name, does string) Tool {
		return Tool{
			Name:        name,
			Description: what(does),
			Properties:  map[string]any{"target": target(vision, false), "modifiers": modifiers},
			Required:    []string{"target"},
		}
	}
	all := []Tool{
		{
			Name:        "navigate",
			Description: "Open a URL in the tab, or go back, forward, or reload.",
			Properties: map[string]any{
				"url": map[string]any{"type": "string", "description": "The URL to open, or back, forward or reload."},
			},
			Required: []string{"url"},
		},
		{
			Name:        "read_page",
			Description: "Read the elements of the page, each on a line with its role, its name, its state and its ref, such as: textbox \"Email\" type=email required [ref_3]. The refs are what the other tools take as a target. Read the page before acting on it, and again after it changes.",
			Properties: map[string]any{
				"filter": map[string]any{"type": "string", "enum": []string{"all", "interactive"}, "description": "interactive for the elements one can act on only; all for every element, the text included. all by default."},
				"depth":  map[string]any{"type": "integer", "description": "How deep into the page to read; 15 by default."},
				"ref":    map[string]any{"type": "string", "description": "Read only the element of this ref and what is in it."},
			},
		},
		{
			Name:        "find",
			Description: "Find the elements of the page that match a query, such as a word of their name or their role, with their refs.",
			Properties: map[string]any{
				"query": map[string]any{"type": "string", "description": "What to look for."},
			},
			Required: []string{"query"},
		},
		{
			Name:        "get_page_text",
			Description: "Read the text of the page, without its elements.",
			Properties:  map[string]any{},
		},
		{
			Name:        "screenshot",
			Description: "See the viewport as an image.",
			Properties:  map[string]any{},
		},
		{
			Name:        "zoom",
			Description: "See a region of the viewport as an image, enlarged.",
			Properties: map[string]any{
				"region": map[string]any{"type": "array", "items": map[string]any{"type": "number"}, "description": "The region, as [x0, y0, x1, y1] in CSS pixels of the viewport."},
			},
			Required: []string{"region"},
		},
		click("left_click", "Click an element or a point."),
		click("right_click", "Right-click an element or a point."),
		click("double_click", "Double-click an element or a point."),
		click("triple_click", "Triple-click an element or a point, as to select the line of text there."),
		{
			Name:        "hover",
			Description: what("Move the mouse over an element or a point."),
			Properties:  map[string]any{"target": target(vision, false)},
			Required:    []string{"target"},
		},
		{
			Name:        "scroll",
			Description: "Turn the mouse wheel at a point of the viewport.",
			Properties: map[string]any{
				"target": map[string]any{
					"type":        "object",
					"description": "The point to scroll at, in CSS pixels of the viewport; its top left corner by default.",
					"properties": map[string]any{
						"type": map[string]any{"type": "string", "enum": []string{"coordinate"}},
						"x":    map[string]any{"type": "number"},
						"y":    map[string]any{"type": "number"},
					},
				},
				"scroll_direction": map[string]any{"type": "string", "enum": []string{"up", "down", "left", "right"}},
				"scroll_amount":    map[string]any{"type": "integer", "description": "How many notches to turn the wheel; 3 by default."},
			},
			Required: []string{"scroll_direction"},
		},
		{
			Name:        "scroll_to",
			Description: "Scroll an element into view.",
			Properties:  map[string]any{"target": target(vision, true)},
			Required:    []string{"target"},
		},
		{
			Name:        "type",
			Description: "Type text into the element that has the focus. Click the field first.",
			Properties: map[string]any{
				"text": map[string]any{"type": "string", "description": "The text to type."},
			},
			Required: []string{"text"},
		},
		{
			Name:        "key",
			Description: "Press keys: one chord or more apart by spaces, each a key name with modifiers joined by +, such as Enter, Tab, ctrl+a or shift+Tab.",
			Properties: map[string]any{
				"text":   map[string]any{"type": "string", "description": "The keys to press."},
				"repeat": map[string]any{"type": "integer", "description": "How many times to press them; once by default."},
			},
			Required: []string{"text"},
		},
		{
			Name:        "form_input",
			Description: "Set the value of a form field, such as a select, a checkbox or a text field, without typing.",
			Properties: map[string]any{
				"target": target(vision, true),
				"value":  map[string]any{"type": "string", "description": "The value: the text or the option of the field, or true or false for a checkbox or a radio button."},
			},
			Required: []string{"target", "value"},
		},
		{
			Name:        "wait",
			Description: "Wait, as for the page to finish something.",
			Properties: map[string]any{
				"duration": map[string]any{"type": "number", "description": "How long to wait, in seconds, up to 30."},
			},
			Required: []string{"duration"},
		},
	}
	var out []Tool
	for _, t := range all {
		if slices.Contains(disabled, t.Name) || (!vision && (t.Name == "screenshot" || t.Name == "zoom")) {
			continue
		}
		out = append(out, t)
	}
	return out
}
