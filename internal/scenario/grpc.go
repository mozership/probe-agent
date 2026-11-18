package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/mozership/probe-agent/internal/llm"
)

// GRPC is a gRPC API, which the tester calls through the grpc action of
// Probe.
type GRPC struct {
	// Addr is the address of the server, which the tester cannot change.
	Addr string
	// Fixed are the metadata the workflow fixes, by name, which the tester
	// cannot change.
	Fixed []string
	// Proto says whether the calls are checked against .proto files.
	Proto bool
	// List lists the services of the server, or, with service, describes
	// its methods, as the grpc action's list does, and returns its res.
	List func(ctx context.Context, service string) (map[string]any, error)
	// Call makes a call through the grpc action once, and returns its
	// answer. A call that gets no status from the server, as one the guard
	// of the run refuses, is an error.
	Call func(ctx context.Context, req GRPCRequest) (GRPCResponse, error)
}

// GRPCRequest is a call of the tester, with the secrets filled in.
type GRPCRequest struct {
	Service  string
	Method   string
	Body     string
	Metadata map[string]string
}

// GRPCResponse is the answer to a call.
type GRPCResponse struct {
	// Code and Message are the status, as NOT_FOUND; Code is empty for a
	// stream the call left before the server ended it.
	Code     string
	Message  string
	Metadata map[string]string
	// Body is the response as JSON, the last of a stream.
	Body string
	// Messages are the responses of a stream, each as JSON; nil for a
	// method whose responses do not stream.
	Messages []string
	// Operation is what the .proto files matched the call to, as
	// users.v1.UserService/GetUser; empty when they matched it to none.
	Operation string
	// Violations are what the .proto files do not allow in the call.
	Violations []Violation
}

// grpcDriver is a gRPC API as a target. Each tool is one call of the grpc
// action.
type grpcDriver struct {
	*ledger
	api GRPC
	// services are what the server listed as the tester starts, or why
	// they could not be listed.
	services   []string
	listFailed string
	// last is the last answer, and lastCall its call, for the judge.
	last     *GRPCResponse
	lastCall string
}

var _ Driver = (*grpcDriver)(nil)

func newGRPCDriver(l *ledger, api GRPC) *grpcDriver {
	return &grpcDriver{ledger: l, api: api}
}

// Kind is grpc.
func (d *grpcDriver) Kind() string { return "grpc" }

// Open lists the services of the server, for the tester to start from. A
// server that cannot be listed is still called: the tester is told why.
func (d *grpcDriver) Open(ctx context.Context) error {
	res, err := d.api.List(ctx, "")
	if err != nil {
		d.listFailed = d.secrets.mask(err.Error())
		return nil
	}
	d.services = stringsOf(res["services"])
	return nil
}

// Offer offers the tools of the API.
func (d *grpcDriver) Offer(opts *llm.ChatOptions) {
	opts.Tools = append(opts.Tools, grpcTools...)
}

// Handles says whether call is of a tool of the API.
func (d *grpcDriver) Handles(call llm.Call) bool {
	return call.Toolset == "" && slices.ContainsFunc(grpcTools, func(t llm.Tool) bool { return t.Name == call.Name })
}

// Brief tells the tester of the API and of the metadata it cannot change.
func (d *grpcDriver) Brief() string {
	var b strings.Builder
	b.WriteString(`The gRPC API at ` + d.secrets.mask(d.api.Addr) + `, through the tools that start with grpc_:
- grpc_list lists the services, and grpc_describe tells the methods of one, with the messages they take and answer. Describe a service before you call it.
- grpc_call calls a method with its request as JSON, the field names as the .proto file writes them or in lowerCamelCase. It returns the status, the metadata and the response as JSON; a method that streams its requests takes a JSON list of them.
- A status other than OK is the server's answer, not an error of the tool.`)
	if len(d.api.Fixed) > 0 {
		b.WriteString("\n- The workflow fixes the metadata " + strings.Join(d.api.Fixed, ", ") + ", which are sent with each call; do not give them.")
	}
	if d.api.Proto {
		b.WriteString("\n- Each call and its response are checked against the .proto files of the API; a call returns what they do not allow in them.")
	}
	return b.String()
}

// Opening says which services the server has.
func (d *grpcDriver) Opening() string {
	addr := d.secrets.mask(d.api.Addr)
	switch {
	case d.listFailed != "":
		return "The services of the gRPC API at " + addr + " could not be listed: " + d.listFailed + "."
	case len(d.services) == 0:
		return "The gRPC API at " + addr + " lists no service."
	}
	return "The gRPC API at " + addr + " has the services " + strings.Join(d.services, ", ") + "."
}

// grpcInput is the input of any tool of the API; each tool reads the
// fields it takes.
type grpcInput struct {
	Service  string            `json:"service"`
	Method   string            `json:"method"`
	Body     json.RawMessage   `json:"body"`
	Metadata map[string]string `json:"metadata"`
}

// Do runs a call of a tool of the API.
func (d *grpcDriver) Do(ctx context.Context, call llm.Call) (llm.Result, Step) {
	step := Step{Tool: call.Name, Target: d.Kind()}
	var raw map[string]any
	if json.Unmarshal(call.Input, &raw) == nil && len(raw) > 0 {
		step.Input, _ = d.secrets.maskAny(raw).(map[string]any)
	}
	res := llm.Result{CallID: call.ID}
	var in grpcInput
	text, err := "", json.Unmarshal(call.Input, &in)
	if err == nil {
		text, err = d.tool(ctx, call.Name, in)
	}
	if err != nil {
		res.Text = "Error: " + d.secrets.mask(err.Error())
		res.IsError = true
		step.Error = res.Text
		return res, step
	}
	res.Text = text
	step.Result = summary(res.Text)
	return res, step
}

// tool runs the tool name with in, and returns what the tester is told,
// masked and cut.
func (d *grpcDriver) tool(ctx context.Context, name string, in grpcInput) (string, error) {
	service, method := strings.TrimSpace(in.Service), strings.TrimSpace(in.Method)
	switch name {
	case "grpc_list":
		res, err := d.api.List(ctx, "")
		if err != nil {
			return "", err
		}
		services := stringsOf(res["services"])
		if len(services) == 0 {
			return "The server lists no service.", nil
		}
		return "The services: " + strings.Join(services, ", ") + ".", nil
	case "grpc_describe":
		if service == "" {
			return "", fmt.Errorf("grpc_describe needs a service")
		}
		res, err := d.api.List(ctx, service)
		if err != nil {
			return "", err
		}
		return truncate(d.secrets.mask(describeService(res)), maxText), nil
	case "grpc_call":
		return d.call(ctx, service, method, in)
	}
	return "", fmt.Errorf("there is no tool %s", name)
}

// call makes a call of the tester.
func (d *grpcDriver) call(ctx context.Context, service, method string, in grpcInput) (string, error) {
	if service == "" || method == "" {
		return "", fmt.Errorf("grpc_call needs a service and a method")
	}
	// The body is JSON, or a string of it; a secret in it is escaped as a
	// JSON string holds it.
	body := strings.TrimSpace(string(in.Body))
	var s string
	if json.Unmarshal(in.Body, &s) == nil {
		body = strings.TrimSpace(s)
	}
	if body == "null" {
		body = ""
	}
	if body != "" && !json.Valid([]byte(body)) {
		return "", fmt.Errorf("the body must be JSON, such as {\"id\": \"1\"}")
	}
	body, err := d.secrets.fillWith(body, func(v string) string { q, _ := json.Marshal(v); return string(q[1 : len(q)-1]) })
	if err != nil {
		return "", err
	}
	md := map[string]string{}
	for _, k := range slices.Sorted(maps.Keys(in.Metadata)) {
		if slices.ContainsFunc(d.api.Fixed, func(f string) bool { return strings.EqualFold(f, k) }) {
			return "", fmt.Errorf("the metadata %s is fixed by the workflow, and sent with each call; leave it out", k)
		}
		v, err := d.secrets.fill(in.Metadata[k])
		if err != nil {
			return "", err
		}
		md[k] = v
	}
	resp, err := d.api.Call(ctx, GRPCRequest{Service: service, Method: method, Body: body, Metadata: md})
	if err != nil {
		return "", err
	}
	named := service + "/" + method
	d.last, d.lastCall = &resp, named
	for _, v := range resp.Violations {
		d.breaches = append(d.breaches, contractBreach{
			Violation: v, step: len(d.steps) + 1, call: "gRPC " + named, operation: resp.Operation, spec: "the .proto files",
		})
	}
	text := truncate(d.secrets.mask(answerText(named, resp)), maxText)
	d.seen(text)
	return text, nil
}

// answerText is what the tester is told of the answer to a call of named.
func answerText(named string, r GRPCResponse) string {
	var b strings.Builder
	code := r.Code
	if code == "" {
		code = "no status: the call left the stream before the server ended it"
	}
	fmt.Fprintf(&b, "%s -> %s", named, code)
	if r.Message != "" {
		b.WriteString(": " + r.Message)
	}
	b.WriteString("\n")
	for _, k := range slices.Sorted(maps.Keys(r.Metadata)) {
		fmt.Fprintf(&b, "%s: %s\n", k, r.Metadata[k])
	}
	// What the .proto files do not allow comes before the response, which a
	// long one would otherwise cut off.
	if len(r.Violations) > 0 {
		b.WriteString("\nThe .proto files do not allow:")
		for _, v := range r.Violations {
			b.WriteString("\n- " + violationLine(v))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	switch {
	case r.Messages != nil:
		fmt.Fprintf(&b, "%d responses streamed:", len(r.Messages))
		for i, m := range r.Messages {
			fmt.Fprintf(&b, "\n[%d] %s", i, m)
		}
	case r.Body == "":
		b.WriteString("No response message.")
	default:
		b.WriteString(r.Body)
	}
	return b.String()
}

// describeService tells the methods of a description, and the messages
// and enums they use, as a .proto file writes them.
func describeService(res map[string]any) string {
	var b strings.Builder
	methods, _ := res["methods"].([]any)
	for _, m := range methods {
		mm, _ := m.(map[string]any)
		req, resp := str(mm["request"]), str(mm["response"])
		if mm["client_streaming"] == true {
			req = "stream " + req
		}
		if mm["server_streaming"] == true {
			resp = "stream " + resp
		}
		fmt.Fprintf(&b, "rpc %s(%s) returns (%s)", str(mm["name"]), req, resp)
		if level := str(mm["idempotency"]); level != "" && level != "IDEMPOTENCY_UNKNOWN" {
			b.WriteString(" // " + level)
		}
		b.WriteString("\n")
	}
	types, _ := res["types"].(map[string]any)
	for _, name := range slices.Sorted(maps.Keys(types)) {
		t, _ := types[name].(map[string]any)
		b.WriteString("\n")
		if t["kind"] == "enum" {
			fmt.Fprintf(&b, "enum %s { %s }\n", name, strings.Join(stringsOf(t["values"]), ", "))
			continue
		}
		fmt.Fprintf(&b, "message %s {\n", name)
		fields, _ := t["fields"].([]any)
		for _, f := range fields {
			ff, _ := f.(map[string]any)
			line := "  "
			if ff["optional"] == true {
				line += "optional "
			}
			line += fmt.Sprintf("%s %s = %v;", str(ff["type"]), str(ff["name"]), ff["number"])
			var notes []string
			if j := str(ff["json_name"]); j != "" && j != str(ff["name"]) {
				notes = append(notes, "json "+j)
			}
			if o := str(ff["oneof"]); o != "" {
				notes = append(notes, "oneof "+o)
			}
			if len(notes) > 0 {
				line += " // " + strings.Join(notes, ", ")
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("}\n")
	}
	if b.Len() == 0 {
		return "The service has no method."
	}
	return strings.TrimRight(b.String(), "\n")
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// stringsOf reads a list of strings.
func stringsOf(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, e := range l {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Final is the last answer of the API. Nothing is called again, as a call
// can change what the API holds.
func (d *grpcDriver) Final(context.Context, bool) observation {
	addr := d.secrets.mask(d.api.Addr)
	o := observation{
		heading: "The last answer of the gRPC API " + addr + ":",
		state:   TargetState{Kind: d.Kind(), Address: addr},
	}
	text := "No call was made."
	if d.last != nil {
		text = truncate(d.secrets.mask(answerText(d.lastCall, *d.last)), 20000)
		o.state.Method = d.lastCall
		o.state.Code = d.last.Code
		o.state.Response = text
	}
	o.sections = []section{{"last_answer", text}}
	return o
}

var grpcTools = []llm.Tool{
	{
		Name:        "grpc_list",
		Description: "List the services of the gRPC API.",
		Properties:  map[string]any{},
	},
	{
		Name:        "grpc_describe",
		Description: "Tell the methods of a service of the gRPC API, and the messages and enums they use, as a .proto file writes them.",
		Properties: map[string]any{
			"service": map[string]any{"type": "string", "description": "The service, by its full name, such as users.v1.UserService."},
		},
		Required: []string{"service"},
	},
	{
		Name:        "grpc_call",
		Description: "Call a method of the gRPC API, and read its answer: the status, the metadata and the response as JSON.",
		Properties: map[string]any{
			"service": map[string]any{"type": "string", "description": "The service, by its full name."},
			"method":  map[string]any{"type": "string", "description": "The method, such as GetUser."},
			"body":    map[string]any{"type": "string", "description": "The request as JSON, such as {\"id\": \"1\"}; a JSON list of the requests for a method that streams them. Empty for an empty request."},
			"metadata": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
				"description":          "Metadata to send, by name, besides those the workflow fixes.",
			},
		},
		Required: []string{"service", "method"},
	},
}
