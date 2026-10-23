// Package page drives one tab of a browser for the agent: it does what the
// members of the browser toolset ask, and records what the page reports
// that the agent does not ask for, such as console errors and failed
// requests.
package page

import "context"

// Page is the tab the agent drives.
type Page interface {
	// Navigate opens url, or goes back, forward or reloads for "back",
	// "forward" and "reload".
	Navigate(ctx context.Context, url string) error
	// Screenshot captures the viewport as JPEG.
	Screenshot(ctx context.Context) ([]byte, error)
	// Zoom captures the region x0, y0 to x1, y1 of the viewport, enlarged,
	// as JPEG.
	Zoom(ctx context.Context, x0, y0, x1, y1 float64) ([]byte, error)
	// Click clicks target count times with button, left, right or middle,
	// holding the modifiers, such as ctrl and shift.
	Click(ctx context.Context, t Target, button string, count int, modifiers []string) error
	Hover(ctx context.Context, t Target) error
	// Scroll turns the wheel at x, y, by amount notches toward direction,
	// up, down, left or right.
	Scroll(ctx context.Context, x, y float64, direction string, amount int) error
	ScrollTo(ctx context.Context, ref string) error
	// Type types text into the element that has the focus.
	Type(ctx context.Context, text string) error
	// Key presses the keys, such as "Enter" or "ctrl+a", or several apart
	// by spaces, repeat times.
	Key(ctx context.Context, keys string, repeat int) error
	// Read returns the elements of the page as lines that end with their
	// refs. See agent.js.
	Read(ctx context.Context, filter string, depth int, ref string) (string, error)
	// Text returns the text the page shows.
	Text(ctx context.Context) (string, error)
	// FormInput sets the value of the form field of ref, and says what it
	// did.
	FormInput(ctx context.Context, ref string, value any) (string, error)
	// State returns the URL and the title of the page.
	State(ctx context.Context) (url, title string, err error)
	// ConsoleErrors returns the errors the page wrote to its console so
	// far.
	ConsoleErrors() []ConsoleError
	// NetworkErrors returns the requests of the page that failed or were
	// answered with a status of 400 or more so far.
	NetworkErrors() []NetworkError
	// Requests returns how many requests the page sent so far, and the
	// slowest of them.
	Requests() Requests
}

// Target is where a pointer action goes: the element of Ref, or the point
// X, Y of the viewport when Ref is empty.
type Target struct {
	Ref  string
	X, Y float64
}

// ConsoleError is an error the page wrote to its console, in the shape the
// browser action reports it.
type ConsoleError struct {
	// Type is error, assert or exception.
	Type string `json:"type"`
	Text string `json:"text"`
	URL  string `json:"url"`
	// Line and Column count from 1, and are 0 when the browser does not
	// say.
	Line   int64 `json:"line"`
	Column int64 `json:"column"`
}

// NetworkError is a request of the page that failed, or that was answered
// with a status of 400 or more.
type NetworkError struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	// Status is 0 for a request that got no response.
	Status int64 `json:"status"`
	// Error is why a request that got no response failed.
	Error string `json:"error,omitempty"`
}

// Requests are what the requests of a page took.
type Requests struct {
	// Count is the requests over HTTP or HTTPS that finished or failed.
	Count int `json:"count"`
	// Slowest are the requests that took longest to finish, the slowest
	// first.
	Slowest []Request `json:"slowest"`
}

// Request is a request that finished, and how long it took from when it
// was sent, its redirects included, to its last byte.
type Request struct {
	Method     string `json:"method"`
	URL        string `json:"url"`
	Status     int64  `json:"status"`
	DurationMS int64  `json:"duration_ms"`
}
