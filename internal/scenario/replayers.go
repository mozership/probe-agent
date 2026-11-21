package scenario

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/mozership/probe-agent/internal/llm"
)

// What a replay of a call of each target that answers checks it gets
// again, and which of the calls only read.

var (
	_ replayer = (*httpDriver)(nil)
	_ replayer = (*grpcDriver)(nil)
	_ replayer = (*graphqlDriver)(nil)
	_ replayer = (*mailDriver)(nil)
	_ replayer = (*dbDriver)(nil)
	_ replayer = (*appDriver)(nil)
)

// outcome of a request is the status code of its response.
func (d *httpDriver) outcome(llm.Call, llm.Result) string {
	if d.last == nil {
		return ""
	}
	return strconv.Itoa(d.last.Code)
}

// reads says whether the request is of a method that only reads.
func (d *httpDriver) reads(call llm.Call) bool {
	var in httpInput
	if json.Unmarshal(call.Input, &in) != nil {
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(in.Method)) {
	case "GET", "HEAD", "OPTIONS":
		return true
	}
	return false
}

// outcome of a call of a method is its status, as OK; that of a listing
// or a description is ok.
func (d *grpcDriver) outcome(call llm.Call, _ llm.Result) string {
	if call.Name != "grpc_call" {
		return "ok"
	}
	if d.last == nil || d.last.Code == "" {
		return "no status"
	}
	return d.last.Code
}

// reads says whether the call lists or describes; a call of a method may
// change what the API holds.
func (d *grpcDriver) reads(call llm.Call) bool { return call.Name != "grpc_call" }

// outcome of a document is the status of the response, and whether it has
// errors; that of a read of the schema is ok.
func (d *graphqlDriver) outcome(call llm.Call, _ llm.Result) string {
	if call.Name != "graphql_query" {
		return "ok"
	}
	if d.last == nil {
		return ""
	}
	if len(d.last.Errors) > 0 {
		return fmt.Sprintf("%d with errors", d.last.Code)
	}
	return strconv.Itoa(d.last.Code)
}

// reads says whether the call reads the schema, or is of a query.
func (d *graphqlDriver) reads(call llm.Call) bool {
	if call.Name != "graphql_query" {
		return true
	}
	var in graphqlInput
	if json.Unmarshal(call.Input, &in) != nil {
		return false
	}
	op := operationOf(in.Query, strings.TrimSpace(in.OperationName))
	return op == "query" || strings.HasPrefix(op, "query ")
}

// outcome of a search is whether a mail matched; that of a read is ok.
func (d *mailDriver) outcome(call llm.Call, res llm.Result) string {
	if call.Name != "mail_search" {
		return "ok"
	}
	if strings.HasPrefix(res.Text, "No mail matches") {
		return "none"
	}
	return "found"
}

// reads is true: the mailbox is only read.
func (d *mailDriver) reads(llm.Call) bool { return true }

// outcome of a query is whether it had rows or an error; that of the
// tables is ok.
func (d *dbDriver) outcome(call llm.Call, res llm.Result) string {
	if call.Name != "db_query" {
		return "ok"
	}
	switch {
	case strings.HasPrefix(res.Text, "The database answered with an error"):
		return "error"
	case strings.HasPrefix(res.Text, "No rows."):
		return "no rows"
	}
	return "rows"
}

// reads is true: the database is only read.
func (d *dbDriver) reads(llm.Call) bool { return true }

// outcome of an action of an app is ok: one that cannot finish, as a tap
// on an element that is not there, is an error, which stops a replay.
func (d *appDriver) outcome(llm.Call, llm.Result) string { return "ok" }

// reads says whether the call reads the screen, or waits for an element.
func (d *appDriver) reads(call llm.Call) bool {
	name, _ := strings.CutPrefix(call.Name, d.p.kind+"_")
	switch name {
	case "tree", "text", "wait", "screenshot":
		return true
	}
	return false
}

// gap says why a value the tester typed into the app is this run's, when
// it is not in the instruction and the tester had seen a screenshot of the
// screen, whose text it may have read; or "".
func (d *appDriver) gap(call llm.Call, in map[string]any, instruction string) string {
	v, _ := in["value"].(string)
	if call.Name != d.p.kind+"_type" || !d.pixels || derived(v, instruction, nil, true) == "" {
		return ""
	}
	return "its value may have been read off a screenshot of the screen"
}
