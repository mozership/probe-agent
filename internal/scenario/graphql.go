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

// GraphQL is a GraphQL API, which the tester calls through probe-graphql.
type GraphQL struct {
	// URL is the endpoint, which the tester cannot change.
	URL string
	// Fixed are the headers the workflow fixes, by name, which the tester
	// cannot change.
	Fixed []string
	// Call sends a document through probe-graphql once, and returns the
	// response. A request that gets no response, as one the guard of the
	// run refuses, is an error.
	Call func(ctx context.Context, req GraphQLRequest) (GraphQLResponse, error)
}

// GraphQLRequest is a document of the tester, with the secrets filled in.
type GraphQLRequest struct {
	Query string
	// Variables are the values of the variables, as a JSON object; empty
	// for none.
	Variables     string
	OperationName string
	Headers       map[string]string
}

// GraphQLResponse is the response to a document.
type GraphQLResponse struct {
	// Code and Status are the HTTP status, as 200 and 200 OK.
	Code   int
	Status string
	// Data is the data of the response as JSON, or empty when it has none,
	// and Errors its errors, each as JSON.
	Data   string
	Errors []string
	// Body is the body, when it is not JSON and so holds no data.
	Body string
}

// graphqlDriver is a GraphQL API as a target. Each tool is one call of
// probe-graphql.
type graphqlDriver struct {
	*ledger
	api GraphQL
	// shownURL is the endpoint as the model and the record see it, with
	// the secrets in it masked.
	shownURL string
	// schema is what the introspection of the API answered, or nil, with
	// why in schemaFailed.
	schema       *gqlSchema
	schemaFailed string
	// last is the last response, and lastOp its operation, for the judge.
	last   *GraphQLResponse
	lastOp string
}

var _ Driver = (*graphqlDriver)(nil)

func newGraphQLDriver(l *ledger, api GraphQL) *graphqlDriver {
	return &graphqlDriver{ledger: l, api: api, shownURL: l.secrets.mask(api.URL)}
}

// Kind is graphql.
func (d *graphqlDriver) Kind() string { return "graphql" }

// Open reads the schema by introspection, for the tester to start from. An
// API that answers no introspection, as many in production do not, is
// still called: the tester is told why the schema could not be read.
func (d *graphqlDriver) Open(ctx context.Context) error {
	d.introspect(ctx)
	// What the tester is told as it starts is what the API showed it.
	d.seen(d.Opening())
	return nil
}

// introspect reads the schema, keeping it, or why it could not be read.
func (d *graphqlDriver) introspect(ctx context.Context) {
	resp, err := d.api.Call(ctx, GraphQLRequest{Query: introspectionQuery, OperationName: "IntrospectionQuery"})
	if err != nil {
		d.schemaFailed = d.secrets.mask(err.Error())
		return
	}
	s, err := readSchema(resp)
	if err != nil {
		d.schemaFailed = d.secrets.mask(err.Error())
		return
	}
	d.schema, d.schemaFailed = s, ""
}

// Offer offers the tools of the API.
func (d *graphqlDriver) Offer(opts *llm.ChatOptions) {
	opts.Tools = append(opts.Tools, graphqlTools...)
}

// Handles says whether call is of a tool of the API.
func (d *graphqlDriver) Handles(call llm.Call) bool {
	return call.Toolset == "" && slices.ContainsFunc(graphqlTools, func(t llm.Tool) bool { return t.Name == call.Name })
}

// Brief tells the tester of the API and of the headers it cannot change.
func (d *graphqlDriver) Brief() string {
	var b strings.Builder
	b.WriteString(`The GraphQL API at ` + d.shownURL + `, through the tools that start with graphql_:
- graphql_schema tells the schema as SDL, whole or one type of it. Read the fields you need before you query them.
- graphql_query sends a query or a mutation, with its variables as a JSON object, and returns the HTTP status, the errors and the data of the response.
- Errors in a response are the API's answer, not an error of the tool.`)
	if len(d.api.Fixed) > 0 {
		b.WriteString("\n- The workflow fixes the headers " + strings.Join(d.api.Fixed, ", ") + ", which are sent with each request; do not give them.")
	}
	return b.String()
}

// Opening says what the schema offers: the fields of its roots.
func (d *graphqlDriver) Opening() string {
	if d.schema == nil {
		return "The schema of the GraphQL API at " + d.shownURL + " could not be read: " + d.schemaFailed + "."
	}
	var parts []string
	for _, root := range []struct{ kind, name string }{
		{"query", d.schema.QueryType.Name}, {"mutation", d.schema.MutationType.Name}, {"subscription", d.schema.SubscriptionType.Name},
	} {
		t := d.schema.typeNamed(root.name)
		if t == nil || len(t.Fields) == 0 {
			continue
		}
		var names []string
		for _, f := range t.Fields {
			names = append(names, f.Name)
		}
		parts = append(parts, "the "+root.kind+" fields "+strings.Join(names, ", "))
	}
	if len(parts) == 0 {
		return "The GraphQL API at " + d.shownURL + " has no field to query."
	}
	return d.secrets.mask("The GraphQL API at " + d.shownURL + " has " + strings.Join(parts, "; ") + ".")
}

// graphqlInput is the input of any tool of the API; each tool reads the
// fields it takes.
type graphqlInput struct {
	Type          string            `json:"type"`
	Query         string            `json:"query"`
	Variables     json.RawMessage   `json:"variables"`
	OperationName string            `json:"operation_name"`
	Headers       map[string]string `json:"headers"`
}

// Do runs a call of a tool of the API.
func (d *graphqlDriver) Do(ctx context.Context, call llm.Call) (llm.Result, Step) {
	step := Step{Tool: call.Name, Target: d.Kind(), URL: d.shownURL}
	var raw map[string]any
	if json.Unmarshal(call.Input, &raw) == nil && len(raw) > 0 {
		step.Input, _ = d.secrets.maskAny(raw).(map[string]any)
	}
	res := llm.Result{CallID: call.ID}
	var in graphqlInput
	text, err := "", json.Unmarshal(call.Input, &in)
	if err == nil {
		switch call.Name {
		case "graphql_schema":
			text, err = d.schemaText(ctx, strings.TrimSpace(in.Type))
			if err == nil {
				d.seen(text)
			}
		case "graphql_query":
			text, err = d.query(ctx, in)
		default:
			err = fmt.Errorf("there is no tool %s", call.Name)
		}
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

// schemaText tells the schema, or the type name of it, as SDL. A schema
// that could not be read as the test started is read again.
func (d *graphqlDriver) schemaText(ctx context.Context, name string) (string, error) {
	if d.schema == nil {
		d.introspect(ctx)
		if d.schema == nil {
			return "", fmt.Errorf("the schema could not be read: %s", d.schemaFailed)
		}
	}
	if name == "" {
		return truncate(d.secrets.mask(d.schema.sdl()), maxText), nil
	}
	t := d.schema.typeNamed(name)
	if t == nil {
		return "", fmt.Errorf("the schema has no type %s; the types are %s", name, strings.Join(d.schema.typeNames(), ", "))
	}
	return truncate(d.secrets.mask(t.sdl()), maxText), nil
}

// query sends a document of the tester.
func (d *graphqlDriver) query(ctx context.Context, in graphqlInput) (string, error) {
	if strings.TrimSpace(in.Query) == "" {
		return "", fmt.Errorf("graphql_query needs a query")
	}
	// A secret in the document or the variables is escaped as a string of
	// either holds it, which is as JSON escapes it.
	escape := func(v string) string { q, _ := json.Marshal(v); return string(q[1 : len(q)-1]) }
	query, err := d.secrets.fillWith(in.Query, escape)
	if err != nil {
		return "", err
	}
	// The variables are a JSON object, or a string of one.
	vars := strings.TrimSpace(string(in.Variables))
	var s string
	if json.Unmarshal(in.Variables, &s) == nil {
		vars = strings.TrimSpace(s)
	}
	if vars == "null" {
		vars = ""
	}
	if vars != "" {
		var obj map[string]any
		if json.Unmarshal([]byte(vars), &obj) != nil {
			return "", fmt.Errorf("the variables must be a JSON object, such as {\"id\": \"1\"}")
		}
	}
	vars, err = d.secrets.fillWith(vars, escape)
	if err != nil {
		return "", err
	}
	headers := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(in.Headers)) {
		if slices.ContainsFunc(d.api.Fixed, func(f string) bool { return strings.EqualFold(f, name) }) {
			return "", fmt.Errorf("the header %s is fixed by the workflow, and sent with each request; leave it out", name)
		}
		v, err := d.secrets.fill(in.Headers[name])
		if err != nil {
			return "", err
		}
		headers[name] = v
	}
	opName := strings.TrimSpace(in.OperationName)
	resp, err := d.api.Call(ctx, GraphQLRequest{Query: query, Variables: vars, OperationName: opName, Headers: headers})
	if err != nil {
		return "", err
	}
	// The operation is named as the tester wrote it, with the secrets
	// masked.
	op := d.secrets.mask(operationOf(in.Query, opName))
	d.last, d.lastOp = &resp, op
	text := truncate(d.secrets.mask(graphqlText(op, resp)), maxText)
	d.seen(text)
	return text, nil
}

// operationOf names the operation of query that the response is to, as
// mutation CreateNote: the one name names, or the first of the document. A
// selection set that starts a definition is a query.
func operationOf(query, name string) string {
	ops := operations(query)
	for _, op := range ops {
		if name == "" || op[1] == name {
			return strings.TrimSpace(op[0] + " " + op[1])
		}
	}
	if name != "" {
		return "operation " + name
	}
	return "query"
}

// operations reads the operations a document defines, each its type and
// its name, which is empty for one that has none. It reads the tokens of
// the document at its top level, past its strings and comments, and does
// not check that the document is valid, which the API does.
func operations(doc string) [][2]string {
	var ops [][2]string
	// braces and parens are how deep the reading is; pending is the
	// keyword of a definition whose selection set has not started.
	braces, parens := 0, 0
	pending, named := "", false
	for i := 0; i < len(doc); {
		c := doc[i]
		switch {
		case c == '#':
			for i < len(doc) && doc[i] != '\n' && doc[i] != '\r' {
				i++
			}
			continue
		case strings.HasPrefix(doc[i:], `"""`):
			end := strings.Index(doc[i+3:], `"""`)
			for end >= 0 && strings.HasSuffix(doc[:i+3+end], `\`) {
				next := strings.Index(doc[i+3+end+3:], `"""`)
				if next < 0 {
					end = -1
					break
				}
				end += 3 + next
			}
			if end < 0 {
				return ops
			}
			i += 3 + end + 3
			continue
		case c == '"':
			i++
			for i < len(doc) && doc[i] != '"' && doc[i] != '\n' {
				if doc[i] == '\\' {
					i++
				}
				i++
			}
			i++
			continue
		case c == '(':
			parens++
		case c == ')':
			parens--
		case c == '{' && parens == 0:
			if braces == 0 {
				switch {
				case pending == "":
					ops = append(ops, [2]string{"query", ""})
				case pending != "fragment" && !named:
					ops = append(ops, [2]string{pending, ""})
				}
				pending, named = "", false
			}
			braces++
		case c == '}' && parens == 0:
			braces--
		case c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z':
			j := i
			for j < len(doc) && (doc[j] == '_' || doc[j] >= 'A' && doc[j] <= 'Z' || doc[j] >= 'a' && doc[j] <= 'z' || doc[j] >= '0' && doc[j] <= '9') {
				j++
			}
			word := doc[i:j]
			if braces == 0 && parens == 0 {
				switch {
				case pending == "" && (word == "query" || word == "mutation" || word == "subscription" || word == "fragment"):
					pending, named = word, false
				case pending != "" && pending != "fragment" && !named && (i == 0 || doc[i-1] != '@' && doc[i-1] != '$'):
					ops = append(ops, [2]string{pending, word})
					named = true
				}
			}
			i = j
			continue
		}
		i++
	}
	return ops
}

// graphqlText is what the tester is told of the response to op.
func graphqlText(op string, r GraphQLResponse) string {
	var b strings.Builder
	status := r.Status
	if status == "" {
		status = fmt.Sprint(r.Code)
	}
	fmt.Fprintf(&b, "%s -> %s\n", op, status)
	// The errors come before the data, which a long one would otherwise
	// cut off.
	if len(r.Errors) > 0 {
		b.WriteString("\nErrors:")
		for _, e := range r.Errors {
			b.WriteString("\n- " + e)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	switch {
	case r.Data != "":
		b.WriteString("Data: " + r.Data)
	case r.Body != "":
		b.WriteString("The body is not JSON:\n" + r.Body)
	default:
		b.WriteString("No data.")
	}
	return b.String()
}

// Final is the last response of the API. Nothing is sent again, as a
// mutation can change what the API holds.
func (d *graphqlDriver) Final(context.Context, bool) observation {
	o := observation{
		heading: "The last response of the GraphQL API " + d.shownURL + ":",
		state:   TargetState{Kind: d.Kind(), URL: d.shownURL},
	}
	text := "No query was sent."
	if d.last != nil {
		text = truncate(d.secrets.mask(graphqlText(d.lastOp, *d.last)), 20000)
		o.state.Method = d.lastOp
		o.state.Status = d.last.Code
		o.state.Response = text
	}
	o.sections = []section{{"last_response", text}}
	return o
}

var graphqlTools = []llm.Tool{
	{
		Name:        "graphql_schema",
		Description: "Tell the schema of the GraphQL API as SDL: the whole of it, or one type with its fields and their arguments.",
		Properties: map[string]any{
			"type": map[string]any{"type": "string", "description": "A type to tell alone, such as Note. Leave it out for the whole schema."},
		},
	},
	{
		Name:        "graphql_query",
		Description: "Send a query or a mutation to the GraphQL API, and read the response: the HTTP status, the errors and the data.",
		Properties: map[string]any{
			"query":          map[string]any{"type": "string", "description": "The document, such as query { note(id: \"1\") { id text } }."},
			"variables":      map[string]any{"type": "string", "description": "The values of the variables the document declares, as a JSON object, such as {\"id\": \"1\"}."},
			"operation_name": map[string]any{"type": "string", "description": "The operation to run, when the document has more than one."},
			"headers": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
				"description":          "Headers to send, by name, besides those the workflow fixes.",
			},
		},
		Required: []string{"query"},
	},
}

// introspectionQuery asks for the schema, deep enough for the types of
// lists of non-null lists of non-null values.
const introspectionQuery = `query IntrospectionQuery {
  __schema {
    queryType { name }
    mutationType { name }
    subscriptionType { name }
    types { ...FullType }
  }
}
fragment FullType on __Type {
  kind name description
  fields(includeDeprecated: true) {
    name description
    args { ...InputValue }
    type { ...TypeRef }
    isDeprecated deprecationReason
  }
  inputFields { ...InputValue }
  interfaces { ...TypeRef }
  enumValues(includeDeprecated: true) { name description isDeprecated deprecationReason }
  possibleTypes { ...TypeRef }
}
fragment InputValue on __InputValue {
  name description
  type { ...TypeRef }
  defaultValue
}
fragment TypeRef on __Type {
  kind name
  ofType { kind name ofType { kind name ofType { kind name ofType { kind name ofType { kind name ofType { kind name ofType { kind name } } } } } } }
}`

// gqlSchema is the schema an introspection answered.
type gqlSchema struct {
	QueryType        gqlName   `json:"queryType"`
	MutationType     gqlName   `json:"mutationType"`
	SubscriptionType gqlName   `json:"subscriptionType"`
	Types            []gqlType `json:"types"`
}

type gqlName struct {
	Name string `json:"name"`
}

type gqlType struct {
	Kind          string       `json:"kind"`
	Name          string       `json:"name"`
	Description   string       `json:"description"`
	Fields        []gqlField   `json:"fields"`
	InputFields   []gqlInput   `json:"inputFields"`
	Interfaces    []gqlTypeRef `json:"interfaces"`
	EnumValues    []gqlEnum    `json:"enumValues"`
	PossibleTypes []gqlTypeRef `json:"possibleTypes"`
}

type gqlField struct {
	Name              string     `json:"name"`
	Description       string     `json:"description"`
	Args              []gqlInput `json:"args"`
	Type              gqlTypeRef `json:"type"`
	IsDeprecated      bool       `json:"isDeprecated"`
	DeprecationReason string     `json:"deprecationReason"`
}

type gqlInput struct {
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	Type         gqlTypeRef `json:"type"`
	DefaultValue *string    `json:"defaultValue"`
}

type gqlEnum struct {
	Name              string `json:"name"`
	Description       string `json:"description"`
	IsDeprecated      bool   `json:"isDeprecated"`
	DeprecationReason string `json:"deprecationReason"`
}

type gqlTypeRef struct {
	Kind   string      `json:"kind"`
	Name   string      `json:"name"`
	OfType *gqlTypeRef `json:"ofType"`
}

// readSchema reads the schema of the response to the introspection query,
// or why it holds none.
func readSchema(r GraphQLResponse) (*gqlSchema, error) {
	var data struct {
		Schema *gqlSchema `json:"__schema"`
	}
	if r.Data != "" {
		if err := json.Unmarshal([]byte(r.Data), &data); err != nil {
			return nil, fmt.Errorf("the answer to the introspection query is not a schema: %w", err)
		}
	}
	if data.Schema != nil {
		return data.Schema, nil
	}
	status := r.Status
	if status == "" {
		status = fmt.Sprint(r.Code)
	}
	if len(r.Errors) > 0 {
		return nil, fmt.Errorf("the introspection query was answered %s with the errors %s", status, strings.Join(r.Errors, "; "))
	}
	return nil, fmt.Errorf("the introspection query was answered %s with no schema", status)
}

// builtinScalars are the scalars every schema has, which SDL leaves out.
var builtinScalars = []string{"String", "Int", "Float", "Boolean", "ID"}

// own says whether t is a type of the schema itself, not one of
// introspection, nor a built-in scalar.
func own(t gqlType) bool {
	return !strings.HasPrefix(t.Name, "__") && !(t.Kind == "SCALAR" && slices.Contains(builtinScalars, t.Name))
}

func (s *gqlSchema) typeNamed(name string) *gqlType {
	if name == "" {
		return nil
	}
	for i := range s.Types {
		if s.Types[i].Name == name {
			return &s.Types[i]
		}
	}
	return nil
}

func (s *gqlSchema) typeNames() []string {
	var names []string
	for _, t := range s.Types {
		if own(t) {
			names = append(names, t.Name)
		}
	}
	slices.Sort(names)
	return names
}

// sdl writes the schema as SDL: its roots, then its types, the roots
// first and the rest by name.
func (s *gqlSchema) sdl() string {
	var b strings.Builder
	b.WriteString("schema {\n")
	for _, root := range []struct{ kind, name string }{
		{"query", s.QueryType.Name}, {"mutation", s.MutationType.Name}, {"subscription", s.SubscriptionType.Name},
	} {
		if root.name != "" {
			fmt.Fprintf(&b, "  %s: %s\n", root.kind, root.name)
		}
	}
	b.WriteString("}\n")
	roots := []string{s.QueryType.Name, s.MutationType.Name, s.SubscriptionType.Name}
	var types []gqlType
	for _, t := range s.Types {
		if own(t) {
			types = append(types, t)
		}
	}
	rank := func(t gqlType) int {
		if i := slices.Index(roots, t.Name); i >= 0 {
			return i
		}
		return len(roots)
	}
	slices.SortStableFunc(types, func(a, b gqlType) int {
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra - rb
		}
		return strings.Compare(a.Name, b.Name)
	})
	for _, t := range types {
		b.WriteString("\n" + t.sdl() + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// sdl writes t as SDL, its description as a comment above it.
func (t *gqlType) sdl() string {
	var b strings.Builder
	comment(&b, "", t.Description)
	switch t.Kind {
	case "SCALAR":
		fmt.Fprintf(&b, "scalar %s", t.Name)
	case "UNION":
		var names []string
		for _, p := range t.PossibleTypes {
			names = append(names, p.Name)
		}
		fmt.Fprintf(&b, "union %s = %s", t.Name, strings.Join(names, " | "))
	case "ENUM":
		fmt.Fprintf(&b, "enum %s {\n", t.Name)
		for _, v := range t.EnumValues {
			comment(&b, "  ", v.Description)
			b.WriteString("  " + v.Name + deprecated(v.IsDeprecated, v.DeprecationReason) + "\n")
		}
		b.WriteString("}")
	case "INPUT_OBJECT":
		fmt.Fprintf(&b, "input %s {\n", t.Name)
		for _, f := range t.InputFields {
			comment(&b, "  ", f.Description)
			b.WriteString("  " + f.sdl() + "\n")
		}
		b.WriteString("}")
	default:
		word := "type"
		if t.Kind == "INTERFACE" {
			word = "interface"
		}
		fmt.Fprintf(&b, "%s %s", word, t.Name)
		if len(t.Interfaces) > 0 {
			var names []string
			for _, i := range t.Interfaces {
				names = append(names, i.Name)
			}
			b.WriteString(" implements " + strings.Join(names, " & "))
		}
		b.WriteString(" {\n")
		for _, f := range t.Fields {
			comment(&b, "  ", f.Description)
			b.WriteString("  " + f.Name)
			if len(f.Args) > 0 {
				var args []string
				for _, a := range f.Args {
					args = append(args, a.sdl())
				}
				b.WriteString("(" + strings.Join(args, ", ") + ")")
			}
			b.WriteString(": " + f.Type.String() + deprecated(f.IsDeprecated, f.DeprecationReason) + "\n")
		}
		b.WriteString("}")
	}
	return b.String()
}

func (i gqlInput) sdl() string {
	s := i.Name + ": " + i.Type.String()
	if i.DefaultValue != nil {
		s += " = " + *i.DefaultValue
	}
	return s
}

// String writes the type as SDL does, as [Note!]!.
func (r gqlTypeRef) String() string {
	inner := "?"
	if r.OfType != nil {
		inner = r.OfType.String()
	}
	switch r.Kind {
	case "NON_NULL":
		return inner + "!"
	case "LIST":
		return "[" + inner + "]"
	}
	return r.Name
}

func deprecated(is bool, reason string) string {
	if !is {
		return ""
	}
	if reason == "" {
		return " @deprecated"
	}
	q, _ := json.Marshal(reason)
	return " @deprecated(reason: " + string(q) + ")"
}

// comment writes a description as a comment, a line for each of its
// lines, indented by indent.
func comment(b *strings.Builder, indent, desc string) {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return
	}
	for line := range strings.SplitSeq(desc, "\n") {
		b.WriteString(strings.TrimRight(indent+"# "+line, " ") + "\n")
	}
}
