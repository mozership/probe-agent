package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/mozership/probe-agent/internal/llm"
)

// HTTP is an HTTP API, which the tester calls through the http action of
// Probe.
type HTTP struct {
	// URL is the base URL: the path of a request is added to it, and the
	// tester cannot leave it.
	URL string
	// Fixed are the headers the workflow fixes, by name, which the tester
	// cannot change.
	Fixed []string
	// OpenAPI is the OpenAPI document the requests are checked against, for
	// the tester to know what it can call; empty when there is none.
	OpenAPI string
	// Call sends a request through the http action once, and returns the
	// response. A request that gets no response, as one the guard of the
	// run refuses, is an error.
	Call func(ctx context.Context, req HTTPRequest) (HTTPResponse, error)
}

// HTTPRequest is a request of the tester, with the secrets filled in.
type HTTPRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    string
}

// HTTPResponse is the response to a request.
type HTTPResponse struct {
	// Code and Status are the status of the response, as 200 and 200 OK.
	Code    int
	Status  string
	Headers map[string]string
	// Body is the body as text, or empty for a body that is not text, which
	// File holds.
	Body string
	File string
	// Operation is what the OpenAPI document matched the request to, as
	// GET /orders/{id}; empty when it matched it to none.
	Operation string
	// Violations are what the OpenAPI document does not allow in the
	// request and the response.
	Violations []Violation
}

// Violation is what the OpenAPI document does not allow in a request or a
// response.
type Violation struct {
	// In is request or response.
	In      string `json:"in"`
	Field   string `json:"field,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message"`
}

// contractBreach is a violation of the OpenAPI document by a step of the
// tester.
type contractBreach struct {
	Violation
	step      int
	method    string
	url       string
	operation string
}

// httpDriver is an HTTP API as a target. Each call of http_request is one
// call of the http action, which keeps the cookies from one to the next.
type httpDriver struct {
	*ledger
	api  HTTP
	base *url.URL
	// shownBase is the base URL as the model and the record see it, with
	// the secrets in it masked.
	shownBase string
	// last is the last response, and lastURL and lastMethod its request,
	// for the judge.
	last       *HTTPResponse
	lastURL    string
	lastMethod string
}

var _ Driver = (*httpDriver)(nil)

func newHTTPDriver(l *ledger, api HTTP) (*httpDriver, error) {
	base, err := url.Parse(api.URL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("the base URL %q of the API must be an http or https URL", api.URL)
	}
	return &httpDriver{ledger: l, api: api, base: base, shownBase: l.secrets.mask(api.URL)}, nil
}

// Kind is http.
func (d *httpDriver) Kind() string { return "http" }

// Open sends nothing: a request is the tester's to make.
func (d *httpDriver) Open(context.Context) error { return nil }

// Offer offers http_request.
func (d *httpDriver) Offer(opts *llm.ChatOptions) {
	opts.Tools = append(opts.Tools, httpRequestTool)
}

// Handles says whether call is of http_request.
func (d *httpDriver) Handles(call llm.Call) bool {
	return call.Toolset == "" && call.Name == httpRequestTool.Name
}

// maxSpec bounds the OpenAPI document the tester is given.
const maxSpec = 60000

// Brief tells the tester of the API, of the headers it cannot change, and
// of what the OpenAPI document says it can call.
func (d *httpDriver) Brief() string {
	var b strings.Builder
	b.WriteString(`The HTTP API at ` + d.shownBase + `, through http_request:
- Give the path after the base URL, such as /orders/42. The request goes to the base URL; another host cannot be reached.
- A request returns the status, the headers and the body of the response. A long body is cut.
- Cookies the API sets are sent on with the requests after.`)
	if len(d.api.Fixed) > 0 {
		b.WriteString("\n- The workflow fixes the headers " + strings.Join(d.api.Fixed, ", ") + ", which are sent with each request; do not give them.")
	}
	if d.api.OpenAPI != "" {
		b.WriteString("\n- Each request and its response are checked against the OpenAPI document below; a request returns what the document does not allow in them.")
		// The document is masked whole before it is cut, as a response is.
		spec := d.secrets.mask(d.api.OpenAPI)
		if len([]rune(spec)) > maxSpec {
			spec = truncate(spec, maxSpec) + "\n(The document is cut here.)"
		}
		b.WriteString("\n\n<openapi>\n" + spec + "\n</openapi>")
	}
	return b.String()
}

// Opening says that no request has been made.
func (d *httpDriver) Opening() string {
	return "No request has been made to the API at " + d.shownBase + "."
}

// httpInput is the input of http_request.
type httpInput struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Query   map[string]string `json:"query"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// Do sends a request of the tester.
func (d *httpDriver) Do(ctx context.Context, call llm.Call) (llm.Result, Step) {
	step := Step{Tool: call.Name, Target: d.Kind()}
	var raw map[string]any
	if json.Unmarshal(call.Input, &raw) == nil && len(raw) > 0 {
		step.Input, _ = d.secrets.maskAny(raw).(map[string]any)
	}
	res := llm.Result{CallID: call.ID}
	var in httpInput
	text, err := "", json.Unmarshal(call.Input, &in)
	if err == nil {
		var u string
		text, u, err = d.request(ctx, in)
		step.URL = d.secrets.mask(u)
	}
	if err != nil {
		res.Text = "Error: " + d.secrets.mask(err.Error())
		res.IsError = true
		step.Error = res.Text
		return res, step
	}
	res.Text = d.secrets.mask(text)
	step.Result = summary(res.Text)
	return res, step
}

// methods are the methods the tester may send.
var methods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}

// request sends in, and returns what the tester is told of the response,
// and the URL it was sent to.
func (d *httpDriver) request(ctx context.Context, in httpInput) (string, string, error) {
	method := strings.ToUpper(strings.TrimSpace(in.Method))
	if !slices.Contains(methods, method) {
		return "", "", fmt.Errorf("the method %q must be one of %s", in.Method, strings.Join(methods, ", "))
	}
	// The URL is shown with the placeholders of the secrets, and sent with
	// their values, each escaped where it stands, so that a value cannot
	// change the path or add to the query.
	shown, err := d.resolve(in.Path, in.Query)
	if err != nil {
		return "", "", err
	}
	u, err := d.fillURL(shown)
	shown = placeholderEscaped.ReplaceAllString(shown, "{secret:$1}")
	if err != nil {
		return "", shown, err
	}
	// A value can make what escaping leaves as it is, such as .., so the
	// path is kept under the base URL once the values are in it too.
	if err := d.within(u); err != nil {
		return "", shown, fmt.Errorf("with the secrets filled in, %w", err)
	}
	headers := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(in.Headers)) {
		if slices.ContainsFunc(d.api.Fixed, func(f string) bool { return strings.EqualFold(f, name) }) {
			return "", shown, fmt.Errorf("the header %s is fixed by the workflow, and sent with each request; leave it out", name)
		}
		v, err := d.secrets.fill(in.Headers[name])
		if err != nil {
			return "", shown, err
		}
		headers[name] = v
	}
	// A secret in a JSON body is escaped as a JSON string holds it.
	isJSON := in.Body != "" && json.Valid([]byte(in.Body))
	escape := func(v string) string { return v }
	if isJSON {
		escape = func(v string) string { q, _ := json.Marshal(v); return string(q[1 : len(q)-1]) }
	}
	body, err := d.secrets.fillWith(in.Body, escape)
	if err != nil {
		return "", shown, err
	}
	if isJSON && !hasHeader(headers, "content-type") && !slices.ContainsFunc(d.api.Fixed, func(f string) bool { return strings.EqualFold(f, "content-type") }) {
		headers["Content-Type"] = "application/json"
	}
	resp, err := d.api.Call(ctx, HTTPRequest{Method: method, URL: u, Headers: headers, Body: body})
	if err != nil {
		return "", shown, err
	}
	d.last, d.lastURL, d.lastMethod = &resp, shown, method
	for _, v := range resp.Violations {
		d.breaches = append(d.breaches, contractBreach{
			Violation: v, step: len(d.steps) + 1, method: method, url: d.secrets.mask(shown), operation: resp.Operation,
		})
	}
	// The response is masked whole before it is cut, so that a secret is
	// not cut into what the mask no longer knows, and what the tester did
	// not see is not kept as what it saw.
	text := truncate(d.secrets.mask(responseText(method, shown, resp)), maxText)
	d.seen(text)
	return text, shown, nil
}

// resolve returns the URL of p under the base URL, with query, and the
// placeholders of secrets escaped as the URL escapes them. A path that
// names another host, or leads out of the base URL, is an error. The
// escapes of p are kept, as %40 in /users/alice%40example.com.
func (d *httpDriver) resolve(p string, query map[string]string) (string, error) {
	p = strings.TrimSpace(p)
	if strings.Contains(p, "://") || strings.HasPrefix(p, "//") {
		return "", fmt.Errorf("the path %q names a host; give the path after the base URL %s", p, d.shownBase)
	}
	rawQuery := ""
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		if p[i] == '?' {
			rawQuery = strings.SplitN(p[i+1:], "#", 2)[0]
		}
		p = p[:i]
	}
	escaped := placeholder.ReplaceAllString(p, "%7Bsecret:$1%7D")
	if _, err := url.PathUnescape(escaped); err != nil {
		return "", fmt.Errorf("the path %q is not escaped as a URL is: %w", p, err)
	}
	u := *d.base
	baseRaw := strings.TrimSuffix(u.EscapedPath(), "/")
	joined := path.Join("/", baseRaw, escaped)
	if strings.HasSuffix(escaped, "/") && joined != "/" {
		joined += "/"
	}
	decoded, _ := url.PathUnescape(joined)
	// The path is kept under the base URL as it is written, and as the
	// server reads it, with its escapes, such as %2e%2e, undone.
	basePath := strings.TrimSuffix(d.base.Path, "/")
	if !under(joined, baseRaw) || !under(path.Clean("/"+decoded), basePath) {
		return "", fmt.Errorf("the path %q leads out of the base URL %s", p, d.shownBase)
	}
	u.Path, u.RawPath = decoded, joined
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", fmt.Errorf("the query of %q: %w", p, err)
	}
	for k, v := range d.base.Query() {
		for _, e := range v {
			q.Add(k, e)
		}
	}
	for k, v := range query {
		q.Set(k, v)
	}
	u.RawQuery, u.Fragment = q.Encode(), ""
	return u.String(), nil
}

// within returns an error when the URL u, as it is sent, leads out of the
// base URL, as the server reads its path, with its escapes undone.
func (d *httpDriver) within(u string) error {
	parsed, err := url.Parse(u)
	if err != nil {
		return err
	}
	if parsed.Scheme != d.base.Scheme || parsed.Host != d.base.Host || !under(path.Clean("/"+parsed.Path), strings.TrimSuffix(d.base.Path, "/")) {
		return fmt.Errorf("the path leads out of the base URL %s", d.shownBase)
	}
	return nil
}

// under says whether p is base or under it.
func under(p, base string) bool {
	return base == "" || p == base || strings.HasPrefix(p, base+"/")
}

// placeholderEscaped is a placeholder of a secret as a URL escapes it: in
// its path, or in its query.
var placeholderEscaped = regexp.MustCompile(`%7Bsecret(?::|%3A)([A-Za-z_][A-Za-z0-9_]*)%7D`)

// fillURL replaces each placeholder in u, as resolve escaped it, with the
// value of its secret, escaped for the path or the query it stands in.
func (d *httpDriver) fillURL(u string) (string, error) {
	pathPart, query, hasQuery := strings.Cut(u, "?")
	pathPart, err := d.fillEscaped(pathPart, url.PathEscape)
	if err != nil || !hasQuery {
		return pathPart, err
	}
	query, err = d.fillEscaped(query, url.QueryEscape)
	return pathPart + "?" + query, err
}

func (d *httpDriver) fillEscaped(s string, escape func(string) string) (string, error) {
	var missing []string
	out := placeholderEscaped.ReplaceAllStringFunc(s, func(m string) string {
		name := placeholderEscaped.FindStringSubmatch(m)[1]
		v, ok := d.secrets.values[name]
		if !ok {
			missing = append(missing, name)
			return m
		}
		return escape(v)
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("no secret is named %s; the secrets are %s", strings.Join(missing, ", "), d.secrets.list())
	}
	return out, nil
}

func hasHeader(h map[string]string, name string) bool {
	for k := range h {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

// responseText is what the tester is told of a response to method at u.
func responseText(method, u string, r HTTPResponse) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s -> %s\n", method, u, statusOf(r))
	for _, name := range slices.Sorted(maps.Keys(r.Headers)) {
		v := r.Headers[name]
		if strings.EqualFold(name, "set-cookie") {
			v = cookieValuesHidden(v)
		}
		fmt.Fprintf(&b, "%s: %s\n", name, v)
	}
	// What the document does not allow comes before the body, which a long
	// one would otherwise cut off.
	if len(r.Violations) > 0 {
		b.WriteString("\nThe OpenAPI document does not allow:")
		for _, v := range r.Violations {
			b.WriteString("\n- " + violationLine(v))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	switch {
	case r.File != "":
		b.WriteString("The body is not text, and was not read.")
	case r.Body == "":
		b.WriteString("The body is empty.")
	default:
		b.WriteString(r.Body)
	}
	return b.String()
}

// cookieValuesHidden hides the values of the cookies a Set-Cookie header
// sets, which can be a session, keeping their names and attributes. The
// http action keeps the cookies for the next request all the same.
func cookieValuesHidden(header string) string {
	var cookies []string
	for _, c := range strings.Split(header, ", ") {
		parts := strings.Split(c, ";")
		if name, _, ok := strings.Cut(parts[0], "="); ok && !strings.Contains(name, " ") {
			parts[0] = name + "=<hidden>"
		}
		cookies = append(cookies, strings.Join(parts, ";"))
	}
	return strings.Join(cookies, ", ")
}

func statusOf(r HTTPResponse) string {
	if r.Status != "" {
		return r.Status
	}
	return fmt.Sprint(r.Code)
}

// violationLine is a violation in one line, as the tester, the judge and
// the reviewer read it.
func violationLine(v Violation) string {
	s := "in the " + v.In
	if v.Field != "" {
		s += ", " + v.Field
	}
	return s + ": " + violationText(v)
}

// violationText is the message of a violation, with its reason, which
// tells what the message may not, as got string, want integer.
func violationText(v Violation) string {
	if v.Reason == "" || strings.Contains(v.Message, v.Reason) {
		return v.Message
	}
	return v.Message + " (" + v.Reason + ")"
}

// Final is the last response of the API. Nothing is sent again, as a
// request can change what the API holds.
func (d *httpDriver) Final(context.Context, bool) observation {
	o := observation{
		heading: "The last response of the HTTP API " + d.shownBase + ":",
		state:   TargetState{Kind: d.Kind()},
	}
	text := "No request was made."
	if d.last != nil {
		text = truncate(d.secrets.mask(responseText(d.lastMethod, d.lastURL, *d.last)), 20000)
		o.state.Method = d.lastMethod
		o.state.URL = d.secrets.mask(d.lastURL)
		o.state.Status = d.last.Code
		o.state.Response = text
	}
	o.sections = []section{{"last_response", text}}
	return o
}

// contractFindings are the findings of the responses the OpenAPI document
// does not allow: a deterministic check found them, so they need no
// evidence of the reviewer. A request that breaks it is the tester's to
// mend, not a finding, and so is one the document has no operation for, as
// a path the tester got wrong. The violations of one field of one
// operation are one finding.
func (l *ledger) contractFindings() []Finding {
	var out []Finding
	index := map[string]int{}
	for _, c := range l.breaches {
		if c.In != "response" || c.operation == "" {
			continue
		}
		op := c.operation
		key := op + "\x00" + c.Field
		i, ok := index[key]
		if !ok {
			title := "The response of " + op + " breaks the OpenAPI document"
			if c.Field != "" {
				title += " at " + c.Field
			}
			title = l.secrets.mask(title)
			out = append(out, Finding{
				Title:    title,
				Severity: "medium",
				Category: "error",
				Expected: "A response the OpenAPI document allows.",
				Steps:    []int{},
				URL:      c.url,
				Verified: true,
			})
			i = len(out) - 1
			index[key] = i
		}
		f := &out[i]
		msg := l.secrets.mask(violationText(c.Violation))
		if !slices.ContainsFunc(f.Evidence, func(e Evidence) bool { return e.Quote == msg }) {
			f.Evidence = append(f.Evidence, Evidence{Quote: msg, Verified: true})
			if f.Observed != "" {
				f.Observed += " "
			}
			f.Observed += msg
		}
		if !slices.Contains(f.Steps, c.step) {
			f.Steps = append(f.Steps, c.step)
		}
	}
	return out
}

// breachLines are the violations of the OpenAPI document, as the judge and
// the reviewer are shown them.
func (l *ledger) breachLines() []string {
	var lines []string
	for _, c := range l.breaches {
		lines = append(lines, fmt.Sprintf("- step %d, %s %s: %s", c.step, c.method, c.url, l.secrets.mask(violationLine(c.Violation))))
	}
	return lines
}

var httpRequestTool = llm.Tool{
	Name:        "http_request",
	Description: "Send a request to the HTTP API, and read its response: the status, the headers and the body.",
	Properties: map[string]any{
		"method": map[string]any{"type": "string", "enum": methods, "description": "The method."},
		"path":   map[string]any{"type": "string", "description": "The path after the base URL, such as /orders/42. A query may be written here or in query."},
		"query": map[string]any{
			"type":                 "object",
			"additionalProperties": map[string]any{"type": "string"},
			"description":          "Query parameters, by name.",
		},
		"headers": map[string]any{
			"type":                 "object",
			"additionalProperties": map[string]any{"type": "string"},
			"description":          "Headers to send, by name, besides those the workflow fixes.",
		},
		"body": map[string]any{"type": "string", "description": "The body as text. A body that is JSON is sent as application/json unless a Content-Type header says otherwise."},
	},
	Required: []string{"method", "path"},
}
